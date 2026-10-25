package user

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"devshard/heightsync"
	"devshard/host"
	"devshard/logging"
	"devshard/types"
)

const (
	heartbeatForceReason = "heartbeat"
	// heartbeatBackoffMaxIntervals caps how many heartbeat intervals a failing host is skipped for.
	heartbeatBackoffMaxIntervals = 8
	// heartbeatSendTimeout bounds one heartbeat send well under the heartbeat interval.
	heartbeatSendTimeout = 5 * time.Second
	// EnvHeightSyncCoalescePerHost toggles transport coalescing of heartbeat
	// spans across slots owned by the same peer (default: true).
	EnvHeightSyncCoalescePerHost = "HEIGHT_SYNC_COALESCE_PER_HOST"
)

func defaultHeartbeatCoalescePerHost() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvHeightSyncCoalescePerHost))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

type composedDiff struct {
	diff    types.Diff
	hostIdx int
}

// MaybeHeartbeat opens a heartbeat turn when due, or flushes ack-carrying
// diffs for an already-open turn. StartHeartbeatLoop calls it on a timer;
// tests and the outbound path may call it directly. Span dispatch does not
// wait for one host before addressing the next (§10.6) and does not abort
// remaining slots on a single send failure. After MsgFinalizeRound the
// session is no longer Active, so this is a no-op.
func (s *Session) MaybeHeartbeat(ctx context.Context) error {
	if s == nil || s.disableHeightSyncHeartbeat {
		return nil
	}
	if s.sm != nil && s.sm.Phase() != types.PhaseActive {
		return nil
	}
	span, err := s.composeHeartbeatSpan()
	// Persist-first: any prefix already in the store must go out even when
	// a later heartbeat fails L0. Catch-up on the next send is not enough
	// if this tick is the one that lands the drain.
	s.dispatchHeartbeatSpan(ctx, span)
	if err != nil {
		s.publishHeightSyncView()
		return err
	}
	err = s.flushHeartbeatAckRounds(ctx)
	s.publishHeightSyncView()
	return err
}

// StartHeartbeatLoop waits for router catalog admission, then runs
// MaybeHeartbeat immediately and again at each cadence deadline until
// StopHeartbeatLoop or Close. The deadline is lastTurnover+Interval after a
// turnover, or turnOpenedAt+TurnTimeout while a turn is still open — not a
// free-running ticker, which opens a full Interval late. Idempotent. A Close
// that races Start does not leave a goroutine behind. In-process clients
// have no catalog URL and skip the wait. When the producer cadence is
// disabled, the height-seed loop still starts and no heartbeat goroutine does.
func (s *Session) StartHeartbeatLoop() {
	if s == nil {
		return
	}
	if s.requireHeightSeed {
		s.startHeightSeedLoop()
	}
	if s.disableHeightSyncHeartbeat {
		logging.Info("heartbeat loop disabled", "subsystem", "heightsync",
			"escrow", s.escrowID)
		return
	}
	s.heartbeatLoopOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.runHeartbeatLoop(ctx)
		}()
		s.mu.Lock()
		if s.heartbeatClosed {
			s.mu.Unlock()
			cancel()
			<-done
			return
		}
		s.heartbeatStop = cancel
		s.heartbeatDone = done
		s.mu.Unlock()
	})
}

// StopHeartbeatLoop cancels the cadence goroutine and waits for it to exit.
// Safe without StartHeartbeatLoop; Close calls it.
func (s *Session) StopHeartbeatLoop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.heartbeatClosed = true
	stop := s.heartbeatStop
	done := s.heartbeatDone
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if done != nil {
		<-done
	}
}

func (s *Session) runHeartbeatLoop(ctx context.Context) {
	if err := s.WaitRouterCatalog(ctx); err != nil {
		logging.Debug("heartbeat loop stopped before catalog", "subsystem", "heightsync",
			"escrow", s.escrowID, "error", err)
		return
	}
	past := s.heartbeatDelay() <= time.Millisecond
	s.tickHeartbeat(ctx)
	delay := s.heartbeatDelayAfterTick(past)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			past = s.heartbeatDelay() <= time.Millisecond
			s.tickHeartbeat(ctx)
			delay = s.heartbeatDelayAfterTick(past)
		case <-s.heartbeatPoke:
			// Turnover landed while sleeping. Re-arm from the new lastTurnover;
			// do not open a span until that deadline.
			delay = s.heartbeatDelay()
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
	}
}

func (s *Session) tickHeartbeat(ctx context.Context) {
	if err := s.MaybeHeartbeat(ctx); err != nil {
		logging.Debug("heartbeat loop tick failed", "subsystem", "heightsync",
			"escrow", s.escrowID, "error", err)
	}
}

// heartbeatDelay is the sleep until the next due check. See Heartbeat.NextWake.
func (s *Session) heartbeatDelay() time.Duration {
	if s == nil || s.heartbeat == nil {
		return heightsync.DefaultHeartbeatInterval
	}
	return s.heartbeat.NextWake(s.now())
}

// heartbeatDelayAfterTick is heartbeatDelay unless the deadline was already
// past when the tick started and the tick left it there. NextWake then
// reports 1ms, and a skipped tick (no height, session no longer active)
// would spin. Poll once per Interval in that case. A deadline that expires
// during the tick is woken immediately so an abandon is not postponed.
func (s *Session) heartbeatDelayAfterTick(alreadyPast bool) time.Duration {
	delay := s.heartbeatDelay()
	if delay > time.Millisecond || !alreadyPast {
		return delay
	}
	interval := heightsync.DefaultHeartbeatInterval
	if s != nil && s.heartbeat != nil {
		if cfg := s.heartbeat.Config().Interval; cfg > 0 {
			interval = cfg
		}
	}
	return interval
}

func (s *Session) now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nowLocked()
}

// pokeHeartbeat asks the loop to recompute its deadline. Non-blocking: a
// turnover that lands inside MaybeHeartbeat is already visible to the re-arm
// after that call returns, so a missed poke there changes nothing.
func (s *Session) pokeHeartbeat() {
	if s == nil || s.heartbeatPoke == nil {
		return
	}
	select {
	case s.heartbeatPoke <- struct{}{}:
	default:
	}
}

// dispatchHeartbeatSpan unicasts every composed heartbeat concurrently so one
// slow or dead host cannot hold the rest of the span. When multiple slots belong
// to the same physical peer, their span diffs are coalesced into a single RPC
// carrying the highest-nonce diff for that peer (with earlier diffs delivered
// inline via catchUpForSend). Failures are logged; the caller still runs the ack
// flush for peers that answered.
func (s *Session) dispatchHeartbeatSpan(ctx context.Context, span []composedDiff) {
	targets := s.coalesceHeartbeatSpan(span)
	if len(targets) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, item := range targets {
		wg.Add(1)
		go func(item composedDiff) {
			defer wg.Done()
			if err := s.sendComposedDiff(ctx, item); err != nil {
				logging.Warn("heartbeat span send failed", "subsystem", "heightsync",
					"escrow", s.escrowID, "nonce", item.diff.Nonce, "host", item.hostIdx, "error", err)
			}
		}(item)
	}
	wg.Wait()
}

// coalesceHeartbeatSpan collapses span entries targeting the same physical peer
// down to the highest-nonce diff for that peer so one roundtrip delivers all of
// its slot heartbeats via inline catch-up.
func (s *Session) coalesceHeartbeatSpan(span []composedDiff) []composedDiff {
	if s == nil || len(span) <= 1 {
		return span
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.heartbeatCoalescePerHost {
		return span
	}
	posByPeer := make(map[int]int, len(span))
	out := make([]composedDiff, 0, len(span))
	for _, item := range span {
		peer := s.canonicalHeartbeatPeerLocked(item.hostIdx)
		if pos, exists := posByPeer[peer]; exists {
			if item.diff.Nonce >= out[pos].diff.Nonce {
				out[pos] = item
			}
			continue
		}
		posByPeer[peer] = len(out)
		out = append(out, item)
	}
	return out
}

func sameHostTransport(a, b HostClient) bool {
	if a == nil || b == nil {
		return false
	}
	if ia, ok := a.(*InProcessClient); ok {
		ib, ok := b.(*InProcessClient)
		return ok && ia != nil && ib != nil && ia.Host != nil && ia.Host == ib.Host
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta == tb && ta.Comparable() {
		return a == b
	}
	return false
}

func (s *Session) sameHeartbeatPeerLocked(i, j int) bool {
	if i == j {
		return true
	}
	if !s.heartbeatCoalescePerHost {
		return false
	}
	if i < 0 || j < 0 || i >= len(s.group) || j >= len(s.group) || i >= len(s.clients) || j >= len(s.clients) {
		return false
	}
	addr := s.group[i].ValidatorAddress
	if addr == "" || addr != s.group[j].ValidatorAddress {
		return false
	}
	return sameHostTransport(s.clients[i], s.clients[j])
}

func (s *Session) canonicalHeartbeatPeerLocked(hostIdx int) int {
	if !s.heartbeatCoalescePerHost || hostIdx <= 0 || hostIdx >= len(s.group) {
		return hostIdx
	}
	for i := 0; i < hostIdx; i++ {
		if s.sameHeartbeatPeerLocked(i, hostIdx) {
			return i
		}
	}
	return hostIdx
}

func (s *Session) heartbeatPeerSlotsLocked(hostIdx int) []int {
	if hostIdx < 0 || hostIdx >= len(s.group) {
		return nil
	}
	if !s.heartbeatCoalescePerHost {
		return []int{hostIdx}
	}
	slots := make([]int, 0, 4)
	for i := range s.group {
		if s.sameHeartbeatPeerLocked(hostIdx, i) {
			slots = append(slots, i)
		}
	}
	return slots
}

func (s *Session) uniqueHeartbeatPeerCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.heartbeatCoalescePerHost {
		return len(s.group)
	}
	seen := make(map[int]struct{}, len(s.group))
	for i := range s.group {
		seen[s.canonicalHeartbeatPeerLocked(i)] = struct{}{}
	}
	return len(seen)
}

// HeartbeatSkippedNoHeight counts Due() calls that skipped because no
// observed height was available (spec §10.3).
func (s *Session) HeartbeatSkippedNoHeight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.heartbeat == nil {
		return 0
	}
	return s.heartbeat.SkippedNoHeight()
}

// HeartbeatTurnovers counts full height-sync round-trips discharged so far,
// whether by heartbeat acks or by executor stamps riding real traffic.
func (s *Session) HeartbeatTurnovers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heartbeat.Turnovers()
}

// HeartbeatTurnTracker is the session's turn view (copy). Tests only.
func (s *Session) HeartbeatTurnTracker() *heightsync.TurnTracker {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnTracker
}

func (s *Session) DriveTimeoutFloor(ctx context.Context) error {
	if s == nil || s.sm == nil || s.sm.Phase() != types.PhaseActive {
		return nil
	}
	s.mu.Lock()
	now := time.Now()
	interval := s.sm.HeartbeatConfig().BlockTime
	if interval <= 0 {
		interval = heightsync.DefaultBlockTime
	}
	if !s.lastTimeoutFloorRound.IsZero() && now.Sub(s.lastTimeoutFloorRound) < interval {
		s.mu.Unlock()
		return nil
	}
	s.lastTimeoutFloorRound = now
	s.mu.Unlock()
	span, err := s.composeSpan(true)
	s.dispatchHeartbeatSpan(ctx, span)
	if err != nil {
		s.publishHeightSyncView()
		return err
	}
	err = s.flushHeartbeatAckRounds(ctx)
	s.publishHeightSyncView()
	return err
}

func (s *Session) composeHeartbeatSpan() ([]composedDiff, error) {
	return s.composeSpan(false)
}

func (s *Session) composeSpan(force bool) ([]composedDiff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.nowLocked()
	hNow, hash, ok := s.referenceStampLocked(s.nonce + 1)
	if !ok || hNow == 0 {
		s.heartbeat.Due(now, 0) // increments skippedNoHeight
		logging.Info("heartbeat skipped", "subsystem", "heightsync",
			"escrow", s.escrowID, "cause", "no_height")
		return nil, nil
	}

	// Turn state is a function of the log. The live tip stamps the span; it
	// must not AdvanceHeight the tracker (that is the dual of the SM oracle
	// fold HeightSyncRepairDue used to do).
	if rec := s.turnTracker.Latest(); rec != nil && rec.State != heightsync.TurnOpen {
		if rec.State == heightsync.TurnDegraded && s.heartbeat.TurnOpen() {
			s.heartbeat.RecordCadence(heightsync.CadenceEvent{
				At:        now,
				Event:     heightsync.CadenceTurnSettledDegraded,
				TurnStart: rec.TurnStart,
				HRef:      rec.HReq,
				Outcome:   rec.State.String(),
			})
		}
		s.heartbeat.SettleTurn()
	}

	due, reason := s.heartbeat.Due(now, hNow)
	if force && !due && !s.heartbeat.TurnOpen() {
		due, reason = true, heightsync.ReasonQuietSession
	}
	if !due {
		s.heartbeat.MaybeRecordDischarged(now, hNow)
		return nil, nil
	}

	// Host-signed pending (confirm/finish/ack) can raise F. Sequencer
	// heartbeats may only copy F, so those raises must land on their own
	// nonces before the span snapshots the floor. Mixing them into heartbeat
	// 1 and freezing H at the old F makes later slots L0-invalid.
	drain, err := s.drainUnpinnedPendingLocked()
	if err != nil {
		return drain, err
	}

	hNow, hash, ok = s.referenceStampLocked(s.nonce + 1)
	if !ok || hNow == 0 {
		return drain, nil
	}

	slots := uint64(len(s.group))
	// The turn's identity is the nonce its first heartbeat lands at, so the
	// producer reports it rather than choosing it. There is no counter to keep in
	// step with the log: a span that never lands leaves nothing behind, and the
	// next attempt is named by wherever it lands instead.
	spanStart := s.nonce + 1
	prev := s.turnTracker.Latest()
	var prevStart uint64
	if prev != nil {
		prevStart = prev.TurnStart
	}
	vector := heightsync.ComposeSyncVector(uint32(slots), prev)
	spanTxs := s.heartbeat.SpanTxs(hNow, hash, slots, reason, vector)
	if len(spanTxs) == 0 {
		return drain, nil
	}

	out := drain
	for i, hbTx := range spanTxs {
		extra := []*types.DevshardTx{hbTx}
		if i == 0 {
			force := s.heartbeatForceTxLocked(s.nonce + 1)
			if force != nil {
				extra = []*types.DevshardTx{force, hbTx}
			}
		}
		diff, hostIdx, err := s.composeDiffLocked(context.Background(), extra)
		if err != nil {
			return out, err
		}
		out = append(out, composedDiff{diff: diff, hostIdx: hostIdx})
	}
	s.heartbeatFlushLeft = 1
	abandoned := s.heartbeat.OpenTurn(now)
	if abandoned {
		s.heartbeat.RecordCadence(heightsync.CadenceEvent{
			At:        now,
			Event:     heightsync.CadenceTurnAbandoned,
			TurnStart: prevStart,
			HRef:      hNow,
			Reason:    string(reason),
		})
	}
	s.heartbeat.RecordCadence(heightsync.CadenceEvent{
		At:        now,
		Event:     heightsync.CadenceHeartbeatOpened,
		TurnStart: spanStart,
		HRef:      hNow,
		Span:      len(spanTxs),
		Reason:    string(reason),
	})
	if s.anchors != nil {
		s.anchors.Record(hNow, heightsync.AnchorKindHeartbeat)
		s.anchors.ObserveTip(hNow)
	}
	logging.Info("heartbeat span dispatched", "subsystem", "heightsync",
		"escrow", s.escrowID, "turn_start", spanStart,
		"height", hNow, "span", len(spanTxs), "drain", len(drain), "reason", string(reason))
	return out, nil
}

// drainUnpinnedPendingLocked persists unpinned pending txs on their own
// nonces so a later heartbeat span can stamp the post-raise floor. Pinned
// finishes stay queued. Caller holds s.mu.
func (s *Session) drainUnpinnedPendingLocked() ([]composedDiff, error) {
	var out []composedDiff
	limit := len(s.pendingTxs) + 1
	for i := 0; i < limit; i++ {
		candidates, _ := s.splitPendingForComposeLocked(nil)
		if len(candidates) == 0 {
			return out, nil
		}
		diff, hostIdx, err := s.composeDiffLocked(context.Background(), nil)
		if err != nil {
			return out, err
		}
		out = append(out, composedDiff{diff: diff, hostIdx: hostIdx})
	}
	return out, nil
}

func (s *Session) flushHeartbeatAckRounds(ctx context.Context) error {
	// Each unique peer returns acks for all of its slots in a single response,
	// so the guaranteed round plus one round per unique peer bounds the loop.
	maxRounds := s.uniqueHeartbeatPeerCount() + 1
	for i := 0; i < maxRounds; i++ {
		s.mu.Lock()
		need := s.heartbeatFlushLeft > 0 || s.hasPendingHeightAckLocked()
		open := false
		if rec := s.turnTracker.Latest(); rec != nil && rec.State == heightsync.TurnOpen {
			open = true
		}
		if !need || !open {
			s.mu.Unlock()
			return nil
		}
		diff, hostIdx, err := s.composeDiffLocked(ctx, nil)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		if s.heartbeatFlushLeft > 0 {
			s.heartbeatFlushLeft--
		}
		s.mu.Unlock()
		if err := s.sendComposedDiff(ctx, composedDiff{diff: diff, hostIdx: hostIdx}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) heartbeatForceTxLocked(nonce uint64) *types.DevshardTx {
	slots := s.heightSyncForceSlotsLocked()
	k := s.heightSyncK
	if k == 0 {
		k = 10
	}
	if k < slots {
		k = slots
	}
	if s.sm.HeightSyncForcedTurnActive(nonce) {
		return nil
	}
	return &types.DevshardTx{Tx: &types.DevshardTx_ForceHeightSyncTurn{
		ForceHeightSyncTurn: &types.MsgForceHeightSyncTurn{
			TriggerNonce: nonce,
			EndNonce:     nonce + slots - 1,
			AnchorK:      k,
			SlotsNum:     slots,
			Reason:       heartbeatForceReason,
		},
	}}
}

func (s *Session) hasPendingHeightAckLocked() bool {
	for _, tx := range s.pendingTxs {
		if tx != nil && tx.GetHeightAck() != nil {
			return true
		}
	}
	return false
}

// referenceStampLocked is the producer side of L0 for the sequencer: a user
// Diff-resident height is **exactly F(nonce)**, or absent.
//
// The user is not a height source (spec §10.3.1). Its own courier tip is a
// collection of peer claims it did not read from any chain itself, so writing it
// into Diff would put a user-chosen integer where the log keeps logical time —
// the whole of P1. It stays where it belongs: on the request-leg envelope, where
// the receiving host judges it against its own follower (|Δ| > D) and can demand
// proof.
//
// Absent is the only other branch, and it is what a session before its first
// inference gets: F does not exist yet, so there is nothing truthful to stamp and
// no heartbeat may open. The first host-signed stamp seeds F and starts the clock.
func (s *Session) referenceStampLocked(nonce uint64) (uint64, []byte, bool) {
	if !s.sm.HeightSyncFloorReady() {
		return 0, nil, false
	}
	floor, floorHash, known := s.sm.HeightSyncFloorAsOf(nonce)
	if !known || floor == 0 || !heightsync.StampPresent(floorHash) {
		return 0, nil, false
	}
	return floor, floorHash, true
}

func (s *Session) observedHeightLocked() (uint64, []byte, bool) {
	if s.observedHeight != nil {
		return s.observedHeight()
	}
	for _, c := range s.clients {
		if src, ok := c.(interface {
			ObservedStampNow() (uint64, []byte, bool)
		}); ok {
			h, hash, ok := src.ObservedStampNow()
			if ok && h > 0 {
				return h, hash, true
			}
			continue
		}
		src, ok := c.(interface{ ObservedHeightNow() (uint64, bool) })
		if !ok {
			continue
		}
		h, ok := src.ObservedHeightNow()
		if ok && h > 0 {
			return h, nil, true
		}
	}
	return 0, nil, false
}

func (s *Session) sendComposedDiff(ctx context.Context, item composedDiff) error {
	s.mu.Lock()
	siblings := s.heartbeatPeerSlotsLocked(item.hostIdx)
	backedOff := s.nowLocked().Before(s.heartbeatRetryAt[item.hostIdx])
	if !backedOff && len(siblings) > 1 {
		var maxSync uint64
		for _, sib := range siblings {
			if s.hostSyncNonce[sib] > maxSync {
				maxSync = s.hostSyncNonce[sib]
			}
		}
		for _, sib := range siblings {
			if maxSync > s.hostSyncNonce[sib] {
				s.hostSyncNonce[sib] = maxSync
			}
		}
	}
	s.mu.Unlock()
	if backedOff {
		return nil
	}

	catchUp, err := s.catchUpForSend(ctx, item.hostIdx, item.diff.Nonce)
	if err != nil {
		logging.Warn("heartbeat catch-up failed", "subsystem", "heightsync",
			"escrow", s.escrowID, "nonce", item.diff.Nonce, "host", item.hostIdx, "error", err)
		return nil
	}

	sendCtx, cancel := context.WithTimeout(ctx, heartbeatSendTimeout)
	resp, err := s.clients[item.hostIdx].Send(sendCtx, host.HostRequest{
		Diffs:            catchUp,
		Nonce:            item.diff.Nonce,
		HeightSyncEscrow: s.heightSyncEscrowHints(),
	}, nil, nil)
	cancel()
	if err != nil {
		backoff := s.noteHeartbeatSendFailure(item.hostIdx)
		logging.Warn("heartbeat send failed", "subsystem", "heightsync",
			"escrow", s.escrowID, "nonce", item.diff.Nonce, "host", item.hostIdx,
			"catch_up", len(catchUp), "timed_out", errors.Is(err, context.DeadlineExceeded),
			"backoff", backoff, "error", err)
		return nil
	}
	s.mu.Lock()
	err = s.processResponse(item.hostIdx, resp, item.diff.Nonce)
	if err == nil && len(siblings) > 1 {
		synced := s.hostSyncNonce[item.hostIdx]
		now := s.nowLocked()
		advanced := false
		for _, sib := range siblings {
			if sib == item.hostIdx {
				continue
			}
			s.noteContactLocked(sib, now)
			if synced > s.hostSyncNonce[sib] {
				s.hostSyncNonce[sib] = synced
				advanced = true
			}
		}
		if advanced {
			s.dropDiffPrefixLocked()
		}
	}
	s.mu.Unlock()
	s.publishHeightSyncView()
	return err
}

// noteHeartbeatSendFailure counts a failed heartbeat send and returns how long
// the peer (and any sibling slots sharing its transport) is skipped for.
func (s *Session) noteHeartbeatSendFailure(hostIdx int) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	siblings := s.heartbeatPeerSlotsLocked(hostIdx)
	failures := s.heartbeatSendFailures[hostIdx] + 1
	backoff := heartbeatBackoff(failures, s.heartbeat.Config().Interval)
	retryAt := s.nowLocked().Add(backoff)
	if len(siblings) == 0 {
		s.heartbeatSendFailures[hostIdx] = failures
		s.heartbeatRetryAt[hostIdx] = retryAt
		return backoff
	}
	for _, sib := range siblings {
		s.heartbeatSendFailures[sib] = failures
		s.heartbeatRetryAt[sib] = retryAt
	}
	return backoff
}

// heartbeatBackoff is zero for a first failure, then doubles per failure from one interval up to the cap.
func heartbeatBackoff(consecutiveFailures int, interval time.Duration) time.Duration {
	if consecutiveFailures < 2 {
		return 0
	}
	intervals := min(1<<min(consecutiveFailures-2, 30), heartbeatBackoffMaxIntervals)
	return time.Duration(intervals) * interval
}

// clearHeartbeatBackoffLocked lets a host that answered be sent heartbeats again. Caller holds s.mu.
func (s *Session) clearHeartbeatBackoffLocked(hostIdx int) {
	if hostIdx < 0 || hostIdx >= len(s.heartbeatSendFailures) {
		return
	}
	s.heartbeatSendFailures[hostIdx] = 0
	s.heartbeatRetryAt[hostIdx] = time.Time{}
}
