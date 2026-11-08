package user

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"devshard/heightsync"
	"devshard/logging"
	"devshard/state"
	"devshard/transport"
	"devshard/types"
)

// heightSyncSeeder is implemented by transport.HTTPClient. In-process clients
// omit it, so the cold-start seed (spec §18.5) is a no-op there.
type heightSyncSeeder interface {
	SeedHeightSync(ctx context.Context) (ok bool, err error)
}

const (
	heightSeedGate               = 30 * time.Second
	heightSeedRetryInitial       = 50 * time.Millisecond
	heightSeedRetryMax           = 5 * time.Second
	heightSeedIncompleteLogEvery = 5 * time.Second
	// WaitHeightSeedChatBudget caps how long a chat request waits on a
	// still-pending seed. Missed returns immediately; the seed keeps running.
	WaitHeightSeedChatBudget = 2 * time.Second
)

// Height seed states on /v1/status and WaitHeightSeed.
const (
	HeightSeedStateOK             = "ok"
	HeightSeedStatePending        = "pending"
	HeightSeedStateMissed         = "missed"
	HeightSeedStateCatalogPending = "catalog_pending"
)

const (
	seedPhaseIdle uint32 = iota
	seedPhaseCatalog
	seedPhasePending
	seedPhaseMissed
	seedPhaseOK
)

type seedVerdict int

const (
	seedAnchored seedVerdict = iota
	seedRetryLater
	seedDeclined
)

func (v seedVerdict) String() string {
	switch v {
	case seedAnchored:
		return "anchored"
	case seedRetryLater:
		return "retry_later"
	case seedDeclined:
		return "declined"
	default:
		return "unknown"
	}
}

// HeightSeedError is the typed refusal WaitHeightSeed returns when the gate
// is on and the roster has not seeded. The gateway maps Code to
// X-Devshard-Error and HTTP 503.
type HeightSeedError struct {
	Code   string
	Seeded int
	Slots  int
}

func (e *HeightSeedError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == transport.DevshardErrorCatalogPending {
		return fmt.Sprintf("router catalog not admitted yet, retry: %d/%d hosts returned a height anchor", e.Seeded, e.Slots)
	}
	return fmt.Sprintf("not seeded yet, retry: %d/%d hosts returned a height anchor", e.Seeded, e.Slots)
}

func (e *HeightSeedError) Is(target error) bool {
	var other *HeightSeedError
	if !errors.As(target, &other) {
		return false
	}
	return e.Code == other.Code
}

// HeightSeedStatus is the snapshot exported on /v1/status as height_seed.
// State is the chat gate: ok, pending, missed, or catalog_pending. Seeded
// counts participants with an applied ack that carries a block, not seed
// RPCs and not slots. Slots is that same roster: distinct validator
// addresses. One block ack from any of a participant's slots counts once.
type HeightSeedStatus struct {
	State        string               `json:"state"`
	Seeded       int                  `json:"seeded"`
	Slots        int                  `json:"slots"`
	SlotOutcomes []HeightSeedSlotView `json:"slot_outcomes,omitempty"`
}

// HeightSeedSlotView is the last per-slot seed verdict for operators.
type HeightSeedSlotView struct {
	Slot    int    `json:"slot"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason,omitempty"`
}

type seedSlotRecord struct {
	verdict seedVerdict
	reason  string
}

// SeedHeightSync fans POST /sessions/:id/height-sync across the roster.
// Success is at least half of unique seed targets returning an Anchor.
// With WithRequireHeightSeed(true) this joins the session seed loop (retry
// forever, 30s gate after catalog admission). With the library default
// (gate off) it runs in the caller until quorum, an unreachable quorum
// (gap 1), or ctx is done. Never returns an error: a miss degrades to
// today's unstamped-nonce-1 path unless the gateway gate refuses chat.
func (s *Session) SeedHeightSync(ctx context.Context) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.requireHeightSeed {
		s.startHeightSeedLoop()
		_ = s.WaitHeightSeed(ctx)
		return
	}
	s.runHeightSeedForeground(ctx)
}

// HeightSeedMissed reports whether the seed is in the missed state (gate
// flipped or quorum unreachable). Tests only. Missed is not terminal when
// the gate is on: the loop keeps retrying.
func (s *Session) HeightSeedMissed() bool {
	if s == nil {
		return false
	}
	return s.heightSeedPhase.Load() == seedPhaseMissed
}

// HeightSeedStatus returns the chat gate. Seeded is the number of participants
// with an applied ack that carries a block. The seed RPC snapshot is SeedRPCStatus.
// The read uses the snapshot published after the last commit and does not
// take the sequencer lock.
func (s *Session) HeightSeedStatus() HeightSeedStatus {
	if s == nil {
		return HeightSeedStatus{State: HeightSeedStateOK}
	}
	return s.floorGateStatus()
}

// SeedRPCStatus is the seed-RPC snapshot. The gateway does not call that RPC.
// /v1/status reports HeightSeedStatus, the floor gate.
func (s *Session) SeedRPCStatus() HeightSeedStatus {
	if s == nil {
		return HeightSeedStatus{State: HeightSeedStateOK}
	}
	s.heightSeedMu.Lock()
	defer s.heightSeedMu.Unlock()
	return s.heightSeedStatusLocked()
}

// MarkHeightSeedGate records catalog admission and the chat-gate deadline.
// The seed loop does this after the router admits the session. A deadline in
// the past is the missed state.
func (s *Session) MarkHeightSeedGate(until time.Time) {
	if s == nil {
		return
	}
	s.heightSeedCatalogAdmitted.Store(true)
	if until.IsZero() {
		s.heightSeedGateUntil.Store(0)
	} else {
		s.heightSeedGateUntil.Store(until.UnixNano())
	}
	s.floorGateNotify()
}

// WaitFloorChat blocks the first chat request until the floor gate is open,
// the bootstrap budget has expired, or ctx is done. The 30s budget is the
// bootstrap cutoff: it becomes missed only while the gate has not opened.
// Opening takes a map time for F and half the participants. After that, a fresh
// floor stays open without the roster count, and a stale floor stays pending
// so one refresh can send a span. After the first inference commits, the
// gate is not consulted. Before the clock starts, the wait is WaitHeightSeedChatBudget
// and the error is catalog_pending.
func (s *Session) WaitFloorChat(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.heightSeedGateUntil.Load() == 0 {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, WaitHeightSeedChatBudget)
			defer cancel()
		}
	}
	refreshed := false
	for {
		st := s.floorGateStatus()
		switch st.State {
		case HeightSeedStateOK:
			return nil
		case HeightSeedStateMissed:
			return heightSeedErrorFromStatus(st)
		}
		if !refreshed && st.State == HeightSeedStatePending {
			refreshed = true
			if err := s.refreshFloorForChat(ctx); err != nil {
				return err
			}
			continue
		}
		ch := s.floorGateWatch()
		st = s.floorGateStatus()
		switch st.State {
		case HeightSeedStateOK:
			return nil
		case HeightSeedStateMissed:
			return heightSeedErrorFromStatus(st)
		}
		var timer *time.Timer
		var timerC <-chan time.Time
		if until := s.heightSeedGateUntil.Load(); until > 0 && !s.floorGateOpened.Load() {
			remain := time.Until(time.Unix(0, until))
			if remain < 0 {
				remain = 0
			}
			timer = time.NewTimer(remain)
			timerC = timer.C
		}
		s.floorGateWaiting.Add(1)
		select {
		case <-ctx.Done():
			s.floorGateWaiting.Add(-1)
			if timer != nil {
				timer.Stop()
			}
			st = s.floorGateStatus()
			if st.State == HeightSeedStateOK {
				return nil
			}
			return heightSeedErrorFromStatus(st)
		case <-ch:
			s.floorGateWaiting.Add(-1)
			if timer != nil {
				timer.Stop()
			}
		case <-timerC:
			s.floorGateWaiting.Add(-1)
		}
	}
}

// WaitFloorWarmup waits until the floor chat gate is open or ctx ends.
// WaitFloorChat returns when the 30s bootstrap cutoff is missed. Warmup
// treats that as not yet: a later ack can still open the gate, and the
// probes run then. A chat request keeps the single WaitFloorChat call.
func (s *Session) WaitFloorWarmup(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		err := s.WaitFloorChat(ctx)
		if err == nil || ctx.Err() != nil {
			return err
		}
		// Subscribe before rechecking so an open that lands in between
		// wakes this wait instead of leaving it on a channel that already fired.
		wait := s.floorGateWatch()
		if s.floorGateStatus().State == HeightSeedStateOK {
			return nil
		}
		select {
		case <-ctx.Done():
			if s.floorGateStatus().State == HeightSeedStateOK {
				return nil
			}
			return s.WaitFloorChat(ctx)
		case <-wait:
		}
	}
}

// floorGateSnap is the part of the chat gate that needs the sequencer lock:
// the floor as of the next nonce, how many participants have acked a block,
// and the cadence interval. Readers apply the clock, the latch, and the
// header map themselves. seeded and slots are participant counts.
type floorGateSnap struct {
	seeded   int
	slots    int
	floor    uint64
	hash     []byte
	interval time.Duration
}

// publishFloorGateLocked stores the gate inputs and wakes waiters. Caller holds s.mu.
func (s *Session) publishFloorGateLocked() {
	if s == nil {
		return
	}
	var ackedSlots []uint32
	if s.sm != nil {
		ackedSlots = s.sm.HeightSyncBlockAckSlots()
	}
	acked, participants := blockAckParticipantCounts(s.group, ackedSlots)
	snap := &floorGateSnap{
		seeded:   acked,
		slots:    participants,
		interval: s.heartbeatIntervalLocked(),
	}
	if s.sm != nil {
		floor, hash, known := s.sm.HeightSyncFloorAsOf(s.nonce + 1)
		if known && floor > 0 && heightsync.StampPresent(hash) {
			snap.floor = floor
			snap.hash = append([]byte(nil), hash...)
		}
	}
	s.floorGate.Store(snap)
	s.floorGateNotify()
}

// floorGateStatus is the chat gate without the sequencer lock.
func (s *Session) floorGateStatus() HeightSeedStatus {
	snap := s.floorGate.Load()
	_, slots := blockAckParticipantCounts(s.group, nil)
	seeded := 0
	interval := heightsync.DefaultHeartbeatInterval
	var floor uint64
	var hash []byte
	if snap != nil {
		slots = snap.slots
		seeded = snap.seeded
		interval = snap.interval
		floor = snap.floor
		hash = snap.hash
	}
	st := HeightSeedStatus{Seeded: seeded, Slots: slots, State: HeightSeedStateOK}
	if s.firstInferenceStarted.Load() {
		return st
	}
	if !s.heightSeedCatalogAdmitted.Load() {
		st.State = HeightSeedStateCatalogPending
		return st
	}
	ms, ok := int64(0), false
	if floor > 0 && s.sm != nil {
		ms, ok = s.sm.LookupHeaderTimeMs(floor, hash)
	}
	// Half the roster is the bootstrap bar. Once that latch is set, a fresh
	// map time is enough: hosts that acked once and then went quiet do not
	// close the gate when their turns are pruned.
	quorum := heightSeedQuorum(seeded, slots)
	fresh := ok && ms > 0 && floorTimeFreshAt(s.gateNow(), ms, interval)
	if fresh && (quorum || s.floorGateOpened.Load()) {
		s.floorGateOpened.Store(true)
		return st
	}
	// Bootstrap is done once F has a map time and half the participants have acked.
	// Freshness is the first inference's wait, so a stale time does not expire
	// the 30s cutoff.
	if ok && ms > 0 && quorum {
		s.floorGateOpened.Store(true)
	}
	if !s.floorGateOpened.Load() {
		if until := s.heightSeedGateUntil.Load(); until > 0 && time.Now().After(time.Unix(0, until)) {
			st.State = HeightSeedStateMissed
			return st
		}
	}
	st.State = HeightSeedStatePending
	return st
}

// gateNow is the heartbeat clock. The function is set before the session is
// shared, so the read does not take s.mu.
func (s *Session) gateNow() time.Time {
	if s == nil || s.clock == nil {
		return time.Now()
	}
	return s.clock()
}

func (s *Session) floorGateNotify() {
	if s == nil {
		return
	}
	s.floorGateMu.Lock()
	defer s.floorGateMu.Unlock()
	if s.floorGateWaiters != nil {
		close(s.floorGateWaiters)
	}
	s.floorGateWaiters = make(chan struct{})
}

func (s *Session) floorGateWatch() <-chan struct{} {
	s.floorGateMu.Lock()
	defer s.floorGateMu.Unlock()
	if s.floorGateWaiters == nil {
		s.floorGateWaiters = make(chan struct{})
	}
	return s.floorGateWaiters
}

func (s *Session) watchHeaderTimes(src state.HeaderTimeSource) {
	var cancel func()
	if w, ok := src.(state.HeaderTimeWatch); ok && w != nil {
		cancel = w.Watch(func() { s.floorGateNotify() })
	}
	s.floorGateMu.Lock()
	prev := s.headerWatchCancel
	s.headerWatchCancel = cancel
	s.floorGateMu.Unlock()
	if prev != nil {
		prev()
	}
}

// floorChatReadyLocked is the chat gate: F as of the next nonce is non-empty,
// the gateway map has a positive time for that pair, and that time is within
// two cadence intervals of the gateway clock. Until the gate has opened, at
// least half the participants also need an applied ack that carries a block.
// The read is LookupHeaderTimeMs. Caller holds s.mu.
func (s *Session) floorChatReadyLocked() bool {
	ready, _ := s.floorChatEvalLocked()
	return ready
}

type floorRefresh uint8

const (
	floorRefreshNone floorRefresh = iota
	floorRefreshBootstrap
	floorRefreshStale
)

// floorChatEvalLocked reports whether chat can open, and which span a stale
// or empty floor still needs. A fresh floor short of half the participants
// needs no span while bootstrap is open. After the gate has opened, that
// half is not required. A missing map time needs no span: compose already
// holds that ack. Caller holds s.mu.
func (s *Session) floorChatEvalLocked() (ready bool, refresh floorRefresh) {
	if s.sm == nil || len(s.group) == 0 {
		return false, floorRefreshNone
	}
	floor, hash, known := s.sm.HeightSyncFloorAsOf(s.nonce + 1)
	if !known || floor == 0 || !heightsync.StampPresent(hash) {
		return false, floorRefreshBootstrap
	}
	ms, ok := s.sm.LookupHeaderTimeMs(floor, hash)
	if !ok || ms <= 0 {
		return false, floorRefreshNone
	}
	if !s.floorTimeFreshLocked(ms) {
		return false, floorRefreshStale
	}
	acked, participants := blockAckParticipantCounts(s.group, s.sm.HeightSyncBlockAckSlots())
	if s.floorGateOpened.Load() || heightSeedQuorum(acked, participants) {
		return true, floorRefreshNone
	}
	return false, floorRefreshNone
}

// floorFreshIntervals is the chat gate's freshness window, in cadence
// intervals. The next span is one interval after the last turnover, and
// time(F) is already behind that turnover by the ack round trip, so a window
// of one interval is stale at the moment the replacement span is sent. Two
// intervals covers that cycle. A floor that has not moved for two intervals
// is a stall.
const floorFreshIntervals = 2

// floorTimeFreshLocked is true when time(F) is within two cadence intervals
// of the gateway clock. The clock is the session heartbeat clock. A floor in
// the future is inside the window. Caller holds s.mu.
func (s *Session) floorTimeFreshLocked(ms int64) bool {
	return floorTimeFreshAt(s.nowLocked(), ms, s.heartbeatIntervalLocked())
}

func floorTimeFreshAt(now time.Time, ms int64, interval time.Duration) bool {
	if interval <= 0 {
		interval = heightsync.DefaultHeartbeatInterval
	}
	age := now.Sub(time.UnixMilli(ms))
	return age <= time.Duration(floorFreshIntervals)*interval
}

func (s *Session) heightSeedStatusLocked() HeightSeedStatus {
	slots := s.heightSeedSlotCount
	seeded := len(s.heightSeedOK)
	st := HeightSeedStatus{Seeded: seeded, Slots: slots, State: s.heightSeedStateLabelLocked()}
	if len(s.heightSeedLast) > 0 {
		views := make([]HeightSeedSlotView, 0, len(s.heightSeedLast))
		for slot, rec := range s.heightSeedLast {
			views = append(views, HeightSeedSlotView{Slot: slot, Verdict: rec.verdict.String(), Reason: rec.reason})
		}
		st.SlotOutcomes = views
	}
	return st
}

func (s *Session) heightSeedStateLabelLocked() string {
	switch s.heightSeedPhase.Load() {
	case seedPhaseOK:
		return HeightSeedStateOK
	case seedPhaseMissed:
		return HeightSeedStateMissed
	case seedPhaseCatalog:
		return HeightSeedStateCatalogPending
	case seedPhasePending:
		return HeightSeedStatePending
	default:
		if !s.requireHeightSeed {
			return HeightSeedStateOK
		}
		if !s.heightSeedCatalogAdmitted.Load() {
			return HeightSeedStateCatalogPending
		}
		return HeightSeedStatePending
	}
}

// WaitHeightSeed joins the seed. Returns nil when the gate is off, there
// are no seeder-capable clients, or quorum is met. Returns a HeightSeedError
// on missed (immediately) or when ctx expires while still short of quorum.
func (s *Session) WaitHeightSeed(ctx context.Context) error {
	if s == nil || !s.requireHeightSeed {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.startHeightSeedLoop()
	for {
		s.heightSeedMu.Lock()
		st := s.heightSeedStatusLocked()
		wait := s.heightSeedWatchLocked()
		s.heightSeedMu.Unlock()
		switch st.State {
		case HeightSeedStateOK:
			return nil
		case HeightSeedStateMissed:
			return heightSeedErrorFromStatus(st)
		}
		select {
		case <-ctx.Done():
			s.heightSeedMu.Lock()
			st = s.heightSeedStatusLocked()
			s.heightSeedMu.Unlock()
			if st.State == HeightSeedStateOK {
				return nil
			}
			return heightSeedErrorFromStatus(st)
		case <-wait:
		}
	}
}

// WaitHeightSeedReady waits until the seed is ok (or the gate is off). Missed
// is not a return: the loop keeps retrying until quorum or ctx is done.
func (s *Session) WaitHeightSeedReady(ctx context.Context) error {
	if s == nil || !s.requireHeightSeed {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.startHeightSeedLoop()
	for {
		err := s.WaitHeightSeed(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		var se *HeightSeedError
		if errors.As(err, &se) && se.Code == transport.DevshardErrorHeightSeedIncomplete {
			s.heightSeedMu.Lock()
			wait := s.heightSeedWatchLocked()
			s.heightSeedMu.Unlock()
			select {
			case <-ctx.Done():
				return s.WaitHeightSeed(ctx)
			case <-wait:
			}
			continue
		}
		s.heightSeedMu.Lock()
		wait := s.heightSeedWatchLocked()
		s.heightSeedMu.Unlock()
		select {
		case <-ctx.Done():
			return err
		case <-wait:
		}
	}
}

func heightSeedErrorFromStatus(st HeightSeedStatus) *HeightSeedError {
	code := transport.DevshardErrorHeightSeedIncomplete
	if st.State == HeightSeedStateCatalogPending {
		code = transport.DevshardErrorCatalogPending
	}
	return &HeightSeedError{Code: code, Seeded: st.Seeded, Slots: st.Slots}
}

// markHeightSeedAdmitted records catalog admission and starts the chat-gate
// clock once. The bootstrap span does this after the router admits the
// session. An explicit SeedHeightSync does it from the seed loop. A second
// caller does not move a deadline that is already set.
func (s *Session) markHeightSeedAdmitted() {
	if s == nil {
		return
	}
	s.heightSeedCatalogAdmitted.Store(true)
	s.heightSeedGateUntil.CompareAndSwap(0, time.Now().Add(heightSeedGate).UnixNano())
}

func (s *Session) startHeightSeedLoop() {
	if s == nil {
		return
	}
	s.heightSeedLoopOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.runHeightSeedLoop(ctx)
		}()
		s.mu.Lock()
		if s.heightSeedClosed {
			s.mu.Unlock()
			cancel()
			<-done
			return
		}
		s.heightSeedStop = cancel
		s.heightSeedDoneCh = done
		s.mu.Unlock()
	})
}

func (s *Session) stopHeightSeedLoop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.heightSeedClosed = true
	stop := s.heightSeedStop
	done := s.heightSeedDoneCh
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if done != nil {
		<-done
	}
}

func (s *Session) runHeightSeedLoop(ctx context.Context) {
	s.setHeightSeedPhase(seedPhaseCatalog)
	if err := s.WaitRouterCatalog(ctx); err != nil {
		logging.Debug("heightsync: seed stopped before catalog",
			heightsync.LogFieldSubsystem, "heightsync",
			"escrow", s.escrowID, "error", err)
		return
	}
	s.markHeightSeedAdmitted()
	s.setHeightSeedPhase(seedPhasePending)
	s.runHeightSeedUntil(ctx, true)
}

func (s *Session) runHeightSeedForeground(ctx context.Context) {
	s.setHeightSeedPhase(seedPhasePending)
	s.runHeightSeedUntil(ctx, false)
}

func (s *Session) runHeightSeedUntil(ctx context.Context, forever bool) {
	s.mu.Lock()
	if s.observedHeight != nil {
		s.mu.Unlock()
		s.setHeightSeedPhase(seedPhaseOK)
		s.heightSeedMissed.Store(false)
		return
	}
	clients := append([]HostClient(nil), s.clients...)
	escrow := s.escrowID
	s.mu.Unlock()
	// Snapshot immediately around each seed RPC. A bootstrap span running
	// beside this loop advances the nonce on its own; that is not the seed
	// writing a diff.
	logCursor := func() (uint64, uint64) {
		s.mu.Lock()
		defer s.mu.Unlock()
		hLast := uint64(0)
		if s.turnTracker != nil {
			hLast = s.turnTracker.LastCompletedHeight()
		}
		return s.nonce, hLast
	}

	targets := uniqueSeedTargets(clients)
	s.heightSeedMu.Lock()
	s.heightSeedSlotCount = len(targets)
	if s.heightSeedOK == nil {
		s.heightSeedOK = make(map[int]struct{})
	}
	if s.heightSeedLast == nil {
		s.heightSeedLast = make(map[int]seedSlotRecord)
	}
	s.heightSeedMu.Unlock()

	if len(targets) == 0 {
		s.setHeightSeedPhase(seedPhaseOK)
		s.heightSeedMissed.Store(false)
		return
	}

	logMutation := func(nonceBefore, hLastBefore uint64) {
		nonceAfter, hLastAfter := logCursor()
		if nonceAfter != nonceBefore || hLastAfter != hLastBefore {
			logging.Warn("heightsync: seed mutated log state",
				heightsync.LogFieldSubsystem, "heightsync",
				"escrow", escrow,
				"nonce_before", nonceBefore,
				"nonce_after", nonceAfter,
				"h_last_before", hLastBefore,
				"h_last_after", hLastAfter)
		}
	}
	observedHeight := func() uint64 {
		s.mu.Lock()
		defer s.mu.Unlock()
		h, _, _ := s.observedHeightLocked()
		return h
	}

	delay := heightSeedRetryInitial
	var lastIncompleteLog time.Time
	for {
		if ctx.Err() != nil {
			return
		}
		s.heightSeedMu.Lock()
		if s.heightSeedPhase.Load() == seedPhaseOK {
			s.heightSeedMu.Unlock()
			return
		}
		pending := unseededHeightTargets(targets, s.heightSeedOK)
		s.heightSeedMu.Unlock()
		if len(pending) == 0 {
			s.finishHeightSeedOK(escrow, targets, observedHeight)
			return
		}

		s.heightSeedMu.Lock()
		alreadySeeded := len(s.heightSeedOK)
		s.heightSeedMu.Unlock()
		nonceBefore, hLastBefore := logCursor()
		attemptCtx, cancel := heightSeedAttemptContext(ctx)
		out := fanHeightSeedUntil(attemptCtx, pending, func(seededThisRound int) bool {
			return heightSeedQuorum(alreadySeeded+seededThisRound, len(targets))
		})
		cancel()

		s.heightSeedMu.Lock()
		applySeedOutcomes(escrow, s.heightSeedOK, s.heightSeedLast, out)
		counts := countSeedVerdicts(targets, s.heightSeedOK, s.heightSeedLast)
		seeded := len(s.heightSeedOK)
		slots := len(targets)
		feasible := heightSeedQuorum(counts.anchored+counts.retryLater, slots)
		quorum := heightSeedQuorum(seeded, slots)
		misses := seedMissSummary(s.heightSeedLast)
		s.heightSeedMu.Unlock()
		s.heightSeedNotify()
		logMutation(nonceBefore, hLastBefore)

		if quorum {
			s.finishHeightSeedOK(escrow, targets, observedHeight)
			// The full-roster sweep anchors the slots quorum went without; the session does not wait for it.
			go func() {
				sweepBeforeNonce, sweepBeforeHeight := logCursor()
				sweepCtx, sweepCancel := heightSeedAttemptContext(ctx)
				defer sweepCancel()
				sweepOut := fanHeightSeed(sweepCtx, targets)
				s.heightSeedMu.Lock()
				applySeedOutcomes(escrow, s.heightSeedOK, s.heightSeedLast, sweepOut)
				s.heightSeedMu.Unlock()
				logMutation(sweepBeforeNonce, sweepBeforeHeight)
			}()
			return
		}

		now := time.Now()
		gateFlipped := false
		if s.requireHeightSeed {
			if until := s.heightSeedGateUntil.Load(); until > 0 && now.After(time.Unix(0, until)) {
				gateFlipped = true
			}
		}
		if !feasible || gateFlipped {
			s.markHeightSeedMissed(escrow, seeded, slots, misses, !feasible)
		}

		if !forever && !feasible {
			return
		}

		if s.heightSeedPhase.Load() == seedPhaseMissed {
			if lastIncompleteLog.IsZero() || now.Sub(lastIncompleteLog) >= heightSeedIncompleteLogEvery {
				logging.Error("heightsync: seed_incomplete",
					heightsync.LogFieldSubsystem, "heightsync",
					"escrow", escrow,
					"seeded", seeded,
					"slots", slots,
					"misses", misses,
					"elapsed", seedElapsed(s.heightSeedGateUntil.Load(), now),
				)
				lastIncompleteLog = now
			}
			delay = heightSeedRetryMax
		} else {
			logging.Debug("heightsync: seed retry",
				heightsync.LogFieldSubsystem, "heightsync",
				"escrow", escrow,
				"sleep", delay.String(),
				"seeded", seeded,
				"slots", slots,
				"pending", len(pending),
				"misses", misses,
			)
		}

		sleep := delay
		if sleep > heightSeedRetryMax {
			sleep = heightSeedRetryMax
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if delay < heightSeedRetryMax {
			delay *= 2
			if delay > heightSeedRetryMax {
				delay = heightSeedRetryMax
			}
		}
	}
}

func seedElapsed(gateUntilNs int64, now time.Time) time.Duration {
	if gateUntilNs == 0 {
		return 0
	}
	start := time.Unix(0, gateUntilNs).Add(-heightSeedGate)
	if now.Before(start) {
		return 0
	}
	return now.Sub(start)
}

func (s *Session) finishHeightSeedOK(escrow string, targets []seedTarget, observedHeight func() uint64) {
	s.heightSeedMu.Lock()
	seeded := len(s.heightSeedOK)
	slots := len(targets)
	s.heightSeedMu.Unlock()
	s.heightSeedMissed.Store(false)
	s.setHeightSeedPhase(seedPhaseOK)
	logging.Info("heightsync: seed_ok",
		heightsync.LogFieldSubsystem, "heightsync",
		"escrow", escrow,
		"seeded", seeded,
		"slots", slots,
		heightsync.LogFieldHeight, observedHeight(),
	)
}

func (s *Session) markHeightSeedMissed(escrow string, seeded, slots int, misses string, unreachable bool) {
	for {
		was := s.heightSeedPhase.Load()
		if was == seedPhaseOK {
			return
		}
		if s.heightSeedPhase.CompareAndSwap(was, seedPhaseMissed) {
			s.heightSeedMissed.Store(true)
			if was != seedPhaseMissed {
				logging.Error("heightsync: seed_missed",
					heightsync.LogFieldSubsystem, "heightsync",
					"escrow", escrow,
					"seeded", seeded,
					"slots", slots,
					"misses", misses,
					"unreachable", unreachable,
				)
			}
			s.heightSeedNotify()
			return
		}
	}
}

func (s *Session) setHeightSeedPhase(phase uint32) {
	for {
		prev := s.heightSeedPhase.Load()
		if prev == seedPhaseOK && phase != seedPhaseOK {
			return
		}
		if s.heightSeedPhase.CompareAndSwap(prev, phase) {
			if prev != phase {
				s.heightSeedNotify()
			}
			return
		}
	}
}

func (s *Session) heightSeedNotify() {
	s.heightSeedMu.Lock()
	defer s.heightSeedMu.Unlock()
	s.heightSeedNotifyLocked()
}

func (s *Session) heightSeedNotifyLocked() {
	if s.heightSeedWaiters != nil {
		close(s.heightSeedWaiters)
	}
	s.heightSeedWaiters = make(chan struct{})
}

func (s *Session) heightSeedWatchLocked() <-chan struct{} {
	if s.heightSeedWaiters == nil {
		s.heightSeedWaiters = make(chan struct{})
	}
	return s.heightSeedWaiters
}

// blockAckParticipantCounts reports how many distinct validator addresses
// have a block ack, and how many distinct addresses the group has. One ack
// from any of a participant's slots counts that participant once. An address
// with no block ack does not count. Slot ids outside the group are ignored.
func blockAckParticipantCounts(group []types.SlotAssignment, ackedSlots []uint32) (acked, total int) {
	if len(group) == 0 {
		return 0, 0
	}
	roster := make(map[string]struct{}, len(group))
	for _, slot := range group {
		roster[slot.ValidatorAddress] = struct{}{}
	}
	hit := make(map[string]struct{}, len(ackedSlots))
	for _, id := range ackedSlots {
		if int(id) >= len(group) {
			continue
		}
		addr := group[id].ValidatorAddress
		if _, ok := roster[addr]; !ok {
			continue
		}
		hit[addr] = struct{}{}
	}
	return len(hit), len(roster)
}

func uniqueSeedTargets(clients []HostClient) []seedTarget {
	seen := make(map[heightSyncSeeder]struct{})
	var targets []seedTarget
	for i, c := range clients {
		seeder, ok := c.(heightSyncSeeder)
		if !ok || seeder == nil {
			continue
		}
		if _, dup := seen[seeder]; dup {
			continue
		}
		seen[seeder] = struct{}{}
		targets = append(targets, seedTarget{slot: i, seeder: seeder})
	}
	return targets
}

// heightSeedQuorum is true at half or more (answered*2 >= total). One
// requires that one answer; two succeed on one; three require two. The chat
// gate passes participants. The seed RPC passes its own target list.
func heightSeedQuorum(seeded, slots int) bool {
	return slots > 0 && seeded*2 >= slots
}

// ChatGateParticipantQuorum is the bootstrap chat rule. acked is the number of
// distinct validator addresses among ackedSlots; participants is the number of
// distinct addresses in the group. One block ack from any of an address's
// slots counts once. A slot outside the group does not count. The gate opens
// at half or more: two participants open on one, three need two.
func ChatGateParticipantQuorum(group []types.SlotAssignment, ackedSlots []uint32) (open bool, acked, participants int) {
	acked, participants = blockAckParticipantCounts(group, ackedSlots)
	return heightSeedQuorum(acked, participants), acked, participants
}

type seedTarget struct {
	slot   int
	seeder heightSyncSeeder
}

type seedOutcome struct {
	slot int
	ok   bool
	err  error
}

type seedRoundCounts struct {
	anchored   int
	retryLater int
	declined   int
}

func heightSeedAttemptTimeout(ctx context.Context) time.Duration {
	timeout := transport.DefaultHeightSeedTimeout
	if dl, ok := ctx.Deadline(); ok {
		left := time.Until(dl)
		if left <= 0 {
			return 0
		}
		if left < timeout {
			return left
		}
	}
	return timeout
}

func heightSeedAttemptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := heightSeedAttemptTimeout(ctx)
	if timeout <= 0 {
		timeout = time.Nanosecond
	}
	return context.WithTimeout(ctx, timeout)
}

func fanHeightSeed(ctx context.Context, targets []seedTarget) []seedOutcome {
	out := make([]seedOutcome, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t seedTarget) {
			defer wg.Done()
			ok, err := t.seeder.SeedHeightSync(ctx)
			out[i] = seedOutcome{slot: t.slot, ok: ok, err: err}
		}(i, t)
	}
	wg.Wait()
	return out
}

// fanHeightSeedUntil seeds targets concurrently and returns the outcomes in
// once isEnough holds for the anchored count, or every target answered; the
// caller cancels ctx to stop the rest.
func fanHeightSeedUntil(ctx context.Context, targets []seedTarget, isEnough func(seeded int) bool) []seedOutcome {
	results := make(chan seedOutcome, len(targets))
	for _, target := range targets {
		go func(target seedTarget) {
			ok, err := target.seeder.SeedHeightSync(ctx)
			results <- seedOutcome{slot: target.slot, ok: ok, err: err}
		}(target)
	}
	out := make([]seedOutcome, 0, len(targets))
	seeded := 0
	for range targets {
		outcome := <-results
		out = append(out, outcome)
		if outcome.ok {
			seeded++
		}
		if isEnough(seeded) {
			break
		}
	}
	return out
}

func classifySeedVerdict(ok bool, err error) (seedVerdict, string) {
	if ok {
		return seedAnchored, "ok"
	}
	if err == nil {
		return seedRetryLater, "omit"
	}
	if errors.Is(err, transport.ErrHTTPSessionRetired) {
		return seedDeclined, err.Error()
	}
	if status, code, _, ok := transport.ConnectApplicationStatus(err); ok {
		if seedHostDeclined(status, code) {
			return seedDeclined, err.Error()
		}
	}
	var status *transport.UpstreamStatusError
	if errors.As(err, &status) {
		if seedHostDeclined(status.StatusCode, status.DevshardError) {
			return seedDeclined, status.Error()
		}
	}
	return seedRetryLater, err.Error()
}

func seedHostDeclined(status int, devshardError string) bool {
	return status == http.StatusNotFound ||
		status == http.StatusNotImplemented ||
		status == http.StatusGone ||
		strings.EqualFold(strings.TrimSpace(devshardError), transport.DevshardErrorNotImplemented) ||
		strings.EqualFold(strings.TrimSpace(devshardError), transport.DevshardErrorHTTPSessionRetired)
}

func applySeedOutcomes(escrow string, okSlots map[int]struct{}, last map[int]seedSlotRecord, out []seedOutcome) {
	for _, r := range out {
		verdict, reason := classifySeedVerdict(r.ok, r.err)
		last[r.slot] = seedSlotRecord{verdict: verdict, reason: reason}
		if verdict == seedAnchored {
			okSlots[r.slot] = struct{}{}
		}
		logging.Debug("heightsync: seed slot",
			heightsync.LogFieldSubsystem, "heightsync",
			"escrow", escrow,
			"slot", r.slot,
			"verdict", verdict.String(),
			"error", reason)
	}
}

func countSeedVerdicts(targets []seedTarget, okSlots map[int]struct{}, last map[int]seedSlotRecord) seedRoundCounts {
	var counts seedRoundCounts
	counts.anchored = len(okSlots)
	for _, t := range targets {
		if _, ok := okSlots[t.slot]; ok {
			continue
		}
		rec, known := last[t.slot]
		if known && rec.verdict == seedDeclined {
			counts.declined++
			continue
		}
		counts.retryLater++
	}
	return counts
}

func seedMissSummary(last map[int]seedSlotRecord) string {
	if len(last) == 0 {
		return ""
	}
	parts := make([]string, 0, len(last))
	for slot, rec := range last {
		if rec.verdict == seedAnchored {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d=%s:%s", slot, rec.verdict.String(), strings.TrimSpace(rec.reason)))
	}
	return strings.Join(parts, ";")
}

func unseededHeightTargets(targets []seedTarget, okSlots map[int]struct{}) []seedTarget {
	var pending []seedTarget
	for _, t := range targets {
		if _, ok := okSlots[t.slot]; !ok {
			pending = append(pending, t)
		}
	}
	return pending
}

// WithRequireHeightSeed joins the old seed-RPC loop. The floor chat gate does
// not read it: the first chat waits for the heartbeat floor either way.
func WithRequireHeightSeed(on bool) SessionOption {
	return func(sess *Session) { sess.requireHeightSeed = on }
}

// SetRequireHeightSeed is the recover-path equivalent of WithRequireHeightSeed.
func (s *Session) SetRequireHeightSeed(on bool) {
	if s == nil {
		return
	}
	s.requireHeightSeed = on
}

// SetDisableHeightSyncHeartbeat is the recover-path equivalent of
// WithDisableHeightSyncHeartbeat.
func (s *Session) SetDisableHeightSyncHeartbeat(on bool) {
	if s == nil {
		return
	}
	s.disableHeightSyncHeartbeat = on
}
