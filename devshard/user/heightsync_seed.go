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
	"devshard/transport"
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

// HeightSeedStatus is the snapshot exported on /v1/status.
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

// HeightSeedStatus returns the operator-facing seed snapshot.
func (s *Session) HeightSeedStatus() HeightSeedStatus {
	if s == nil {
		return HeightSeedStatus{State: HeightSeedStateOK}
	}
	s.heightSeedMu.Lock()
	defer s.heightSeedMu.Unlock()
	return s.heightSeedStatusLocked()
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
	s.heightSeedCatalogAdmitted.Store(true)
	if s.requireHeightSeed {
		s.heightSeedGateUntil.Store(time.Now().Add(heightSeedGate).UnixNano())
	}
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
	nonceBefore := s.nonce
	hLastBefore := uint64(0)
	if s.turnTracker != nil {
		hLastBefore = s.turnTracker.LastCompletedHeight()
	}
	s.mu.Unlock()

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

	logMutation := func() {
		s.mu.Lock()
		nonceAfter := s.nonce
		hLastAfter := uint64(0)
		if s.turnTracker != nil {
			hLastAfter = s.turnTracker.LastCompletedHeight()
		}
		s.mu.Unlock()
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

		attemptCtx, cancel := heightSeedAttemptContext(ctx)
		out := fanHeightSeed(attemptCtx, pending)
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
		logMutation()

		if quorum {
			sweepCtx, sweepCancel := heightSeedAttemptContext(ctx)
			sweepOut := fanHeightSeed(sweepCtx, targets)
			sweepCancel()
			s.heightSeedMu.Lock()
			applySeedOutcomes(escrow, s.heightSeedOK, s.heightSeedLast, sweepOut)
			s.heightSeedMu.Unlock()
			logMutation()
			s.finishHeightSeedOK(escrow, targets, observedHeight)
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

		sleep := min(delay, heightSeedRetryMax)
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

// heightSeedQuorum is true when at least half of slots returned an Anchor
// (seeded*2 >= slots). One slot still requires that one Anchor; two slots
// succeed on one; three require two.
func heightSeedQuorum(seeded, slots int) bool {
	return slots > 0 && seeded*2 >= slots
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

func classifySeedVerdict(ok bool, err error) (seedVerdict, string) {
	if ok {
		return seedAnchored, "ok"
	}
	if err == nil {
		return seedRetryLater, "omit"
	}
	var status *transport.UpstreamStatusError
	if errors.As(err, &status) {
		if status.StatusCode == http.StatusNotFound ||
			status.StatusCode == http.StatusNotImplemented ||
			strings.EqualFold(strings.TrimSpace(status.DevshardError), transport.DevshardErrorNotImplemented) {
			return seedDeclined, status.Error()
		}
	}
	return seedRetryLater, err.Error()
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

// WithRequireHeightSeed turns the fail-closed chat/warmup gate on. Default
// off in package user so existing tests keep today's degrade path.
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
