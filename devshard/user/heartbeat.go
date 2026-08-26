package user

import (
	"context"
	"sync"
	"time"

	"devshard/heightsync"
	"devshard/host"
	"devshard/logging"
	"devshard/types"
)

const heartbeatForceReason = "heartbeat"

type composedDiff struct {
	diff    types.Diff
	hostIdx int
}

// MaybeHeartbeat opens a heartbeat turn when due, or flushes ack-carrying
// diffs for an already-open turn. StartHeartbeatLoop calls it on a timer;
// tests and the outbound path may call it directly. Span dispatch does not
// wait for one host before addressing the next (§10.6) and does not abort
// remaining slots on a single send failure.
func (s *Session) MaybeHeartbeat(ctx context.Context) error {
	if s.sm != nil && s.sm.Phase() != types.PhaseActive {
		return nil
	}
	s.ensureHeightSeed(ctx)
	span, err := s.composeHeartbeatSpan()
	if err != nil {
		s.publishHeightSyncView()
		return err
	}
	s.dispatchHeartbeatSpan(ctx, span)
	err = s.flushHeartbeatAckRounds(ctx)
	s.publishHeightSyncView()
	return err
}

// StartHeartbeatLoop runs MaybeHeartbeat immediately and then every Interval
// until StopHeartbeatLoop or Close. Idempotent. A Close that races Start does
// not leave a goroutine behind.
func (s *Session) StartHeartbeatLoop() {
	if s == nil {
		return
	}
	s.heartbeatLoopOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		interval := s.heartbeat.Config().Interval
		if interval <= 0 {
			interval = heightsync.DefaultHeartbeatInterval
		}
		go func() {
			defer close(done)
			s.runHeartbeatLoop(ctx, interval)
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

func (s *Session) runHeartbeatLoop(ctx context.Context, interval time.Duration) {
	if err := s.MaybeHeartbeat(ctx); err != nil {
		logging.Debug("heartbeat loop tick failed", "subsystem", "heightsync",
			"escrow", s.escrowID, "error", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.MaybeHeartbeat(ctx); err != nil {
				logging.Debug("heartbeat loop tick failed", "subsystem", "heightsync",
					"escrow", s.escrowID, "error", err)
			}
		}
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

func (s *Session) composeHeartbeatSpan() ([]composedDiff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.nowLocked()
	// The span's heartbeats land at consecutive nonces starting here, and they
	// all carry the same height, so one floor read at the first nonce satisfies
	// L0 for the whole span: each later heartbeat clears a floor its own
	// predecessor set, with equality.
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
				At:      now,
				Event:   heightsync.CadenceTurnSettledDegraded,
				TurnSeq: rec.TurnSeq,
				HRef:    rec.HReq,
				Outcome: rec.State.String(),
			})
		}
		s.heartbeat.SettleTurn()
	}

	due, reason := s.heartbeat.Due(now, hNow)
	if !due {
		s.heartbeat.MaybeRecordDischarged(now, hNow)
		return nil, nil
	}

	slots := uint64(len(s.group))
	prevSeq := s.heartbeatTurnSeq
	s.heartbeatTurnSeq++
	prev := s.turnTracker.Record(prevSeq)
	vector := heightsync.ComposeSyncVector(uint32(slots), prev)
	spanTxs := s.heartbeat.SpanTxs(s.heartbeatTurnSeq, hNow, hash, slots, reason, vector)
	if len(spanTxs) == 0 {
		return nil, nil
	}

	out := make([]composedDiff, 0, len(spanTxs))
	for i, hbTx := range spanTxs {
		extra := []*types.DevshardTx{hbTx}
		if i == 0 {
			force := s.heartbeatForceTxLocked(s.nonce + 1)
			if force != nil {
				extra = []*types.DevshardTx{force, hbTx}
			}
		}
		diff, hostIdx, err := s.composeDiffLocked(extra)
		if err != nil {
			return nil, err
		}
		out = append(out, composedDiff{diff: diff, hostIdx: hostIdx})
	}
	s.heartbeatFlushLeft = 1
	abandoned := s.heartbeat.OpenTurn(now)
	if abandoned {
		s.heartbeat.RecordCadence(heightsync.CadenceEvent{
			At:      now,
			Event:   heightsync.CadenceTurnAbandoned,
			TurnSeq: prevSeq,
			HRef:    hNow,
			Reason:  string(reason),
		})
	}
	s.heartbeat.RecordCadence(heightsync.CadenceEvent{
		At:      now,
		Event:   heightsync.CadenceHeartbeatOpened,
		TurnSeq: s.heartbeatTurnSeq,
		HRef:    hNow,
		Span:    len(out),
		Reason:  string(reason),
	})
	if s.anchors != nil {
		s.anchors.Record(hNow, heightsync.AnchorKindHeartbeat)
		s.anchors.ObserveTip(hNow)
	}
	logging.Info("heartbeat span dispatched", "subsystem", "heightsync",
		"escrow", s.escrowID, "turn_seq", s.heartbeatTurnSeq,
		"height", hNow, "span", len(out), "reason", string(reason))
	return out, nil
}

func (s *Session) flushHeartbeatAckRounds(ctx context.Context) error {
	// Every slot acks at most once per turn, so the guaranteed round plus one
	// round per slot bounds the loop without a cadence parameter.
	maxRounds := len(s.group) + 1
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
		diff, hostIdx, err := s.composeDiffLocked(nil)
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
	slots := uint64(len(s.group))
	if s.heightSyncSlots != 0 {
		slots = s.heightSyncSlots
	}
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

// referenceStampLocked is the producer side of L0 for the sequencer: a
// Diff-resident height is max(own view, F(nonce)), or absent. It is never below
// F(nonce), so an honest sequencer cannot author a regression.
//
// This covers MsgStartInference and MsgHeartbeat alike — every height in Diff is
// a reference height (spec §14), and the sequencer is a carrier on both.
//
// A floor beyond W_conf of the sequencer's own view is declined rather than
// carried, which leaves the caller with the same situation as an unusable oracle:
// the start leg goes out unstamped and the heartbeat is skipped. Both are states
// the protocol already handles — hosts arm close-ready when stamps stop — and
// both are better than the sequencer signing a height it has no reason to think
// exists.
func (s *Session) referenceStampLocked(nonce uint64) (uint64, []byte, bool) {
	h, hash, ok := s.observedHeightLocked()
	if !heightsync.StampPresent(hash) {
		h, hash, ok = 0, nil, false
	}
	floor, floorHash, known := s.sm.HeightSyncFloorAsOf(nonce)
	if known && floor > h && heightsync.StampPresent(floorHash) {
		if s.heartbeatCfg.FloorOutOfReach(floor, h) {
			logging.Warn("height stamp omitted: floor out of reach", "subsystem", "heightsync",
				"escrow", s.escrowID, "nonce", nonce, "floor", floor, "own_tip", h)
			return 0, nil, false
		}
		return floor, floorHash, true
	}
	return h, hash, ok
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
	catchUp := s.diffsForHost(item.hostIdx)
	s.mu.Unlock()

	resp, err := s.clients[item.hostIdx].Send(ctx, host.HostRequest{
		Diffs:            catchUp,
		Nonce:            item.diff.Nonce,
		HeightSyncEscrow: s.heightSyncEscrowHints(),
	}, nil, nil)
	if err != nil {
		logging.Warn("heartbeat host dead", "subsystem", "heightsync",
			"escrow", s.escrowID, "nonce", item.diff.Nonce, "host", item.hostIdx, "error", err)
		return nil
	}
	s.mu.Lock()
	err = s.processResponse(item.hostIdx, resp, item.diff.Nonce)
	s.mu.Unlock()
	s.publishHeightSyncView()
	return err
}
