package user

import (
	"context"

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
// diffs for an already-open turn. It is the E2 quiet-session / outbound-round
// hook: call it on a block tick or before other outbound work.
func (s *Session) MaybeHeartbeat(ctx context.Context) error {
	s.ensureHeightSeed(ctx)
	span, err := s.composeHeartbeatSpan()
	if err != nil {
		return err
	}
	for _, item := range span {
		if err := s.sendComposedDiff(ctx, item); err != nil {
			return err
		}
	}
	return s.flushHeartbeatAckRounds(ctx)
}

// HeartbeatSkippedNoHeight is the H3 counter.
func (s *Session) HeartbeatSkippedNoHeight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.heartbeat == nil {
		return 0
	}
	return s.heartbeat.SkippedNoHeight()
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

	hNow, hash, ok := s.observedHeightLocked()
	if !ok || hNow == 0 {
		if s.heartbeat != nil {
			s.heartbeat.Due(0, 0) // increments skippedNoHeight
		}
		logging.Info("heartbeat skipped", "subsystem", "heightsync",
			"escrow", s.escrowID, "cause", "no_height")
		return nil, nil
	}

	s.turnTracker.AdvanceHeight(hNow)
	if rec := s.turnTracker.Latest(); rec != nil && rec.State == heightsync.TurnOpen {
		return nil, nil
	}

	due, reason := s.heartbeat.Due(hNow, s.turnTracker.LastCompletedHeight())
	if !due {
		return nil, nil
	}

	slots := uint64(len(s.group))
	s.heartbeatTurnSeq++
	prev := s.turnTracker.Record(s.heartbeatTurnSeq - 1)
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
		s.turnTracker.Observe(diff.Nonce, diff.Txs, hNow)
		out = append(out, composedDiff{diff: diff, hostIdx: hostIdx})
	}
	cfg := s.heartbeat.Config()
	flush := int(cfg.MinRoundsPerBlock) - 1
	if flush < 0 {
		flush = 0
	}
	s.heartbeatFlushLeft = flush
	logging.Info("heartbeat span dispatched", "subsystem", "heightsync",
		"escrow", s.escrowID, "turn_seq", s.heartbeatTurnSeq,
		"height", hNow, "span", len(out), "reason", string(reason))
	return out, nil
}

func (s *Session) flushHeartbeatAckRounds(ctx context.Context) error {
	for {
		s.mu.Lock()
		hNow, _, ok := s.observedHeightLocked()
		if !ok {
			hNow = 0
		}
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
		if hNow > 0 {
			s.turnTracker.Observe(diff.Nonce, diff.Txs, hNow)
		}
		if s.heartbeatFlushLeft > 0 {
			s.heartbeatFlushLeft--
		}
		s.mu.Unlock()
		if err := s.sendComposedDiff(ctx, composedDiff{diff: diff, hostIdx: hostIdx}); err != nil {
			return err
		}
	}
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
	defer s.mu.Unlock()
	if err := s.processResponse(item.hostIdx, resp, item.diff.Nonce); err != nil {
		return err
	}
	hNow, _, ok := s.observedHeightLocked()
	if ok && hNow > 0 {
		s.turnTracker.Observe(item.diff.Nonce, item.diff.Txs, hNow)
	}
	return nil
}
