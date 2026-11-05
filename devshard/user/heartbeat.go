package user

import (
	"context"
	"errors"
	"sort"
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
)

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
// have no catalog URL and skip the wait. The unstamped bootstrap span starts
// after catalog admission. This loop does not call the height-sync seed RPC.
// When the producer cadence is disabled, no heartbeat goroutine starts; the
// bootstrap span still does.
func (s *Session) StartHeartbeatLoop() {
	if s == nil {
		return
	}
	s.startBootstrapSpan()
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
	bootstrapStop := s.bootstrapStop
	bootstrapDone := s.bootstrapDone
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	if bootstrapStop != nil {
		bootstrapStop()
	}
	if done != nil {
		<-done
	}
	if bootstrapDone != nil {
		<-bootstrapDone
	}
}

// startBootstrapSpan waits for router catalog admission, then sends one
// unstamped heartbeat per slot while F is empty. Until a host ack raises F,
// it retries once per heartbeat interval: the still-open bootstrap turn gets
// its unacked heartbeats resent, and a closed one gets a new span. The seed
// gate starts it, including when the quiet-session cadence is off. A session
// that already has a floor sends nothing here.
func (s *Session) startBootstrapSpan() {
	if s == nil {
		return
	}
	s.bootstrapOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			if err := s.WaitRouterCatalog(ctx); err != nil {
				logging.Debug("bootstrap span stopped before catalog", "subsystem", "heightsync",
					"escrow", s.escrowID, "error", err)
				return
			}
			s.markHeightSeedAdmitted()
			s.runBootstrapLoop(ctx)
		}()
		s.mu.Lock()
		if s.heartbeatClosed {
			s.mu.Unlock()
			cancel()
			<-done
			return
		}
		s.bootstrapStop = cancel
		s.bootstrapDone = done
		s.mu.Unlock()
	})
}

// runBootstrapLoop sends the bootstrap span, then retries each heartbeat
// interval until F is set, the session leaves Active, or ctx ends. Once F is
// set, the ordinary cadence owns the floor.
func (s *Session) runBootstrapLoop(ctx context.Context) {
	for {
		if err := s.sendBootstrapSpan(ctx); err != nil {
			logging.Debug("bootstrap span failed", "subsystem", "heightsync",
				"escrow", s.escrowID, "error", err)
		}
		if s.bootstrapSettled() {
			return
		}
		timer := time.NewTimer(s.bootstrapRetryInterval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// bootstrapSettled reports that the bootstrap loop has nothing left to do:
// F is set or the session is no longer Active.
func (s *Session) bootstrapSettled() bool {
	if s.sm == nil || s.sm.Phase() != types.PhaseActive {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hNow, _, ok := s.referenceStampLocked(s.nonce + 1)
	return ok && hNow != 0
}

func (s *Session) bootstrapRetryInterval() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heartbeatIntervalLocked()
}

// heartbeatIntervalLocked is the cadence interval. Caller holds s.mu.
func (s *Session) heartbeatIntervalLocked() time.Duration {
	if s.heartbeat != nil {
		if cfg := s.heartbeat.Config().Interval; cfg > 0 {
			return cfg
		}
	}
	return heightsync.DefaultHeartbeatInterval
}

// refreshFloorForChat sends the span the chat gate is waiting on. An empty
// floor gets the unstamped opening span. A floor older than two cadence
// intervals gets an ordinary span that copies F, so a newer host tip can
// raise it. Chats of this session share that span: a second one inside the
// same interval sends nothing and waits for the acks. A fresh floor, or a
// floor whose map time is missing, sends nothing. Catalog admission is
// unchanged: the caller only invokes this once the gate is pending.
func (s *Session) refreshFloorForChat(ctx context.Context) error {
	if s == nil || s.sm == nil || s.sm.Phase() != types.PhaseActive {
		return nil
	}
	s.mu.Lock()
	_, mode := s.floorChatEvalLocked()
	s.mu.Unlock()
	switch mode {
	case floorRefreshBootstrap:
		return s.sendBootstrapSpan(ctx)
	case floorRefreshStale:
		s.mu.Lock()
		recent := s.floorSpanRecentLocked(s.nowLocked())
		s.mu.Unlock()
		if recent {
			return nil
		}
		span, err := s.composeSpan(true)
		s.dispatchHeartbeatSpan(ctx, span)
		if err != nil {
			s.publishHeightSyncView()
			return err
		}
		err = s.flushHeartbeatAckRounds(ctx)
		s.publishHeightSyncView()
		return err
	default:
		return nil
	}
}

// floorSpanRecentLocked reports that this session opened a height span within
// the last cadence interval. Caller holds s.mu.
func (s *Session) floorSpanRecentLocked(now time.Time) bool {
	if s.lastFloorSpanAt.IsZero() {
		return false
	}
	return now.Sub(s.lastFloorSpanAt) < s.heartbeatIntervalLocked()
}

// sendBootstrapSpan composes and dispatches the opening span when F is empty,
// then flushes the acks those heartbeats solicit. A later call, after one ack
// has raised F, does not send another unstamped span.
func (s *Session) sendBootstrapSpan(ctx context.Context) error {
	if s == nil || s.sm == nil || s.sm.Phase() != types.PhaseActive {
		return nil
	}
	span, err := s.composeBootstrapOnly()
	s.dispatchHeartbeatSpan(ctx, span)
	if err != nil {
		s.publishHeightSyncView()
		return err
	}
	err = s.flushHeartbeatAckRounds(ctx)
	s.publishHeightSyncView()
	return err
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
// slow or dead host cannot hold the rest of the span. Failures are logged;
// the caller still runs the ack flush for slots that answered.
func (s *Session) dispatchHeartbeatSpan(ctx context.Context, span []composedDiff) {
	if len(span) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, item := range span {
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

// composeBootstrapOnly sends the opening span while F is empty and returns
// nothing once a host stamp has raised it. Caller does not hold s.mu.
func (s *Session) composeBootstrapOnly() ([]composedDiff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hNow, _, ok := s.referenceStampLocked(s.nonce + 1)
	if ok && hNow != 0 {
		return nil, nil
	}
	return s.composeBootstrapSpanLocked(s.nowLocked())
}

// composeBootstrapSpanLocked emits one unstamped heartbeat per slot. The
// gateway's polled tip is not written onto the message. Attempts are spaced
// one heartbeat interval apart, so concurrent callers emit one span. A retry
// while the bootstrap turn is still open resends that turn's unacked
// heartbeats on their original nonces; an unstamped turn never closes by
// deadline, and those acks still count toward it. Caller holds s.mu.
func (s *Session) composeBootstrapSpanLocked(now time.Time) ([]composedDiff, error) {
	if !s.bootstrapSentAt.IsZero() && now.Before(s.bootstrapSentAt.Add(s.heartbeatIntervalLocked())) {
		return nil, nil
	}
	// No drain. A queued confirm or finish stays queued for the next start
	// or an explicit drain. This span is only the unstamped heartbeats.
	hNow, _, ok := s.referenceStampLocked(s.nonce + 1)
	if ok && hNow != 0 {
		return nil, nil
	}
	if resend, ok := s.bootstrapResendLocked(); ok {
		s.bootstrapSentAt = now
		s.bootstrapAckFlush = true
		logging.Info("bootstrap span resent", "subsystem", "heightsync",
			"escrow", s.escrowID, "turn_start", s.turnTracker.Latest().TurnStart,
			"unacked", len(resend))
		return resend, nil
	}

	slots := uint64(len(s.group))
	spanStart := s.nonce + 1
	prev := s.turnTracker.Latest()
	var prevStart uint64
	if prev != nil {
		prevStart = prev.TurnStart
	}
	vector := heightsync.ComposeSyncVector(uint32(slots), prev)
	spanTxs := s.heartbeat.BootstrapSpanTxs(slots, vector)
	if len(spanTxs) == 0 {
		return nil, nil
	}

	// Keep a queued confirm or finish out of these diffs. The opening span
	// is heartbeats only; drain and the next start publish the rest.
	//
	// No MsgForceHeightSyncTurn. Nonces 1..slots are already a sync window,
	// so each host still attaches a response anchor. A force turn would
	// require a request anchor, and this span goes out before any reply has
	// filled the peer-tip cache.
	parked := s.detachNonAckPendingLocked()
	out := []composedDiff{}
	for _, hbTx := range spanTxs {
		diff, hostIdx, err := s.composeDiffLocked(context.Background(), []*types.DevshardTx{hbTx})
		if err != nil {
			s.restoreParkedPendingLocked(parked)
			return out, err
		}
		out = append(out, composedDiff{diff: diff, hostIdx: hostIdx})
	}
	s.restoreParkedPendingLocked(parked)
	s.heartbeatFlushLeft = 1
	s.bootstrapAckFlush = true
	s.bootstrapSentAt = now
	s.lastFloorSpanAt = now
	abandoned := s.heartbeat.OpenTurn(now)
	if abandoned {
		s.heartbeat.RecordCadence(heightsync.CadenceEvent{
			At:        now,
			Event:     heightsync.CadenceTurnAbandoned,
			TurnStart: prevStart,
			Reason:    string(heightsync.ReasonQuietSession),
		})
	}
	s.heartbeat.RecordCadence(heightsync.CadenceEvent{
		At:        now,
		Event:     heightsync.CadenceHeartbeatOpened,
		TurnStart: spanStart,
		Span:      len(spanTxs),
		Reason:    string(heightsync.ReasonQuietSession),
	})
	logging.Info("heartbeat span dispatched", "subsystem", "heightsync",
		"escrow", s.escrowID, "turn_start", spanStart,
		"height", uint64(0), "span", len(spanTxs), "reason", string(heightsync.ReasonQuietSession))
	return out, nil
}

// bootstrapResendLocked returns the heartbeats of the open bootstrap turn
// whose slot has not acked, each addressed to its original host. ok is false
// when the latest turn is not an open unstamped turn, or when a heartbeat
// diff can no longer be read; the caller then composes a new span. Caller
// holds s.mu.
func (s *Session) bootstrapResendLocked() ([]composedDiff, bool) {
	if s.turnTracker == nil || len(s.group) == 0 {
		return nil, false
	}
	rec := s.turnTracker.Latest()
	if rec == nil || rec.State != heightsync.TurnOpen || rec.HReq != 0 {
		return nil, false
	}
	slots := uint64(len(s.group))
	var out []composedDiff
	for n := rec.RequestSpan[0]; n <= rec.RequestSpan[1] && n <= s.nonce; n++ {
		if _, acked := rec.Acks[heightsync.SlotForNonce(n, slots)]; acked {
			continue
		}
		diffs, err := s.diffRangeLocked(n, n)
		if err != nil || len(diffs) != 1 {
			return nil, false
		}
		out = append(out, composedDiff{diff: diffs[0], hostIdx: int(n % slots)})
	}
	return out, true
}

func (s *Session) composeSpan(force bool) ([]composedDiff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.nowLocked()
	hNow, hash, ok := s.referenceStampLocked(s.nonce + 1)
	if !ok || hNow == 0 {
		return s.composeBootstrapSpanLocked(now)
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
	stampTime, err := s.resolveStampTimeLocked(hNow, hash, s.floorStampCandidateLocked(s.nonce+1, hNow))
	if err != nil {
		return drain, err
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
	for _, tx := range spanTxs {
		if hb := tx.GetHeartbeat(); hb != nil {
			hb.ObservedTimeMs = stampTime
		}
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
	s.lastFloorSpanAt = now
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
	if s == nil || !s.ackFlushInFlight.CompareAndSwap(false, true) {
		return nil
	}
	defer s.ackFlushInFlight.Store(false)
	// Clear the park flag on every exit, including compose and send errors.
	// This defer runs before the in-flight flag drops, so the next flush
	// does not observe a stale park.
	defer func() {
		s.mu.Lock()
		s.bootstrapAckFlush = false
		s.mu.Unlock()
	}()
	// Every slot acks at most once per turn, so the guaranteed round plus one
	// round per slot bounds the loop without a cadence parameter.
	maxRounds := len(s.group) + 1
	for i := 0; i < maxRounds; i++ {
		s.mu.Lock()
		var parked parkedPending
		if s.bootstrapAckFlush {
			parked = s.detachNonAckPendingLocked()
		}
		need := s.heartbeatFlushLeft > 0 || s.hasPendingHeightAckLocked()
		open := false
		if rec := s.turnTracker.Latest(); rec != nil && rec.State == heightsync.TurnOpen {
			open = true
		}
		if !need || !open {
			s.restoreParkedPendingLocked(parked)
			s.mu.Unlock()
			return nil
		}
		diff, hostIdx, err := s.composeDiffLocked(ctx, nil)
		s.restoreParkedPendingLocked(parked)
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

// indexedPending is a queued tx and the index it had in pendingTxs.
type indexedPending struct {
	tx    *types.DevshardTx
	index int
}

// parkedPending is the split detachNonAckPendingLocked makes. parked txs
// stay out of the ack flush. ackAt remembers where the acks sat so restore
// can put every survivor back at its original index.
type parkedPending struct {
	parked []indexedPending
	ackAt  []indexedPending
}

// detachNonAckPendingLocked pulls every queued tx that is not a height ack
// out of the compose set. The opening span's flush uses it so a confirm or
// finish that was already waiting is not published by that span. Caller holds
// s.mu.
func (s *Session) detachNonAckPendingLocked() parkedPending {
	if len(s.pendingTxs) == 0 {
		return parkedPending{}
	}
	acks := make([]*types.DevshardTx, 0, len(s.pendingTxs))
	var split parkedPending
	for i, tx := range s.pendingTxs {
		if tx == nil {
			continue
		}
		if tx.GetHeightAck() != nil {
			acks = append(acks, tx)
			split.ackAt = append(split.ackAt, indexedPending{tx: tx, index: i})
			continue
		}
		split.parked = append(split.parked, indexedPending{tx: tx, index: i})
	}
	s.pendingTxs = acks
	s.reindexPendingLocked()
	return split
}

// restoreParkedPendingLocked puts parked txs back among the acks that are
// still queued, each at the index it had before the park. An ack the flush
// applied is absent. Caller holds s.mu.
func (s *Session) restoreParkedPendingLocked(split parkedPending) {
	if len(split.parked) == 0 {
		return
	}
	surviving := make(map[*types.DevshardTx]struct{}, len(s.pendingTxs))
	for _, tx := range s.pendingTxs {
		if tx != nil {
			surviving[tx] = struct{}{}
		}
	}
	merged := make([]indexedPending, 0, len(split.parked)+len(split.ackAt))
	seen := make(map[*types.DevshardTx]struct{}, len(split.ackAt))
	for _, ack := range split.ackAt {
		if _, ok := surviving[ack.tx]; !ok {
			continue
		}
		merged = append(merged, ack)
		seen[ack.tx] = struct{}{}
	}
	merged = append(merged, split.parked...)
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].index < merged[j].index
	})
	out := make([]*types.DevshardTx, 0, len(merged)+len(s.pendingTxs))
	for _, item := range merged {
		out = append(out, item.tx)
	}
	for _, tx := range s.pendingTxs {
		if tx == nil {
			continue
		}
		if _, ok := seen[tx]; ok {
			continue
		}
		out = append(out, tx)
	}
	s.pendingTxs = out
	s.reindexPendingLocked()
}

func (s *Session) reindexPendingLocked() {
	clear(s.pendingTxKeys)
	for _, tx := range s.pendingTxs {
		if key := devshardTxKey(tx); key != "" {
			s.pendingTxKeys[key] = struct{}{}
		}
	}
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
// Absent is the branch a session gets before its first host stamp: F does
// not exist yet, so a heartbeat may not carry a height. The opening span is
// the unstamped exception. The first host-signed stamp seeds F.
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
	backedOff := s.nowLocked().Before(s.heartbeatRetryAt[item.hostIdx])
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
	s.mu.Unlock()
	s.publishHeightSyncView()
	return err
}

// noteHeartbeatSendFailure counts a failed heartbeat send and returns how long the host is skipped for.
func (s *Session) noteHeartbeatSendFailure(hostIdx int) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeatSendFailures[hostIdx]++
	backoff := heartbeatBackoff(s.heartbeatSendFailures[hostIdx], s.heartbeat.Config().Interval)
	s.heartbeatRetryAt[hostIdx] = s.nowLocked().Add(backoff)
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
