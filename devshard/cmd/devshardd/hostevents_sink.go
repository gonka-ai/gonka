package main

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"devshard/bridge"
	devshardbridge "devshard/cmd/devshardd/bridge"
	devshardstorage "devshard/storage"
)

const (
	// rehydrateEscrowCheckTimeout bounds one chain query in the reset sweep.
	rehydrateEscrowCheckTimeout = 5 * time.Second
	// rehydrateSweepBudget bounds the whole sweep. It runs inline on the
	// host-events loop, so an unresponsive chain must not stall event delivery
	// for open_escrows*timeout. Escrows past the budget are re-checked on the
	// next reset.
	rehydrateSweepBudget = 30 * time.Second
	// rehydrateMinInterval is the floor between two sweeps.
	rehydrateMinInterval = time.Minute
)

// escrowWarmSink implements hostevents.Sink: it prefetches chain escrow metadata
// into escrow_cache when the DAPI host-events long-poll reports an escrow-created
// event, so a later first inference can bind without a request-time chain fetch.
//
// It uses the live (non-caching) bridge so the warm write always reflects chain
// truth; the caching bridge is only used on the read/bind side.
type escrowWarmSink struct {
	bridge    bridge.MainnetBridge
	store     devshardstorage.Storage
	log       *slog.Logger
	onSettled func(escrowID string) error

	mu            sync.Mutex
	lastRehydrate time.Time
}

func newEscrowWarmSink(b bridge.MainnetBridge, store devshardstorage.Storage, log *slog.Logger, onSettled func(string) error) *escrowWarmSink {
	if log == nil {
		log = slog.Default()
	}
	return &escrowWarmSink{bridge: b, store: store, log: log, onSettled: onSettled}
}

// WarmEscrow fetches escrow metadata from chain and caches it for lazy bind.
func (s *escrowWarmSink) WarmEscrow(escrowID string) error {
	info, err := s.bridge.GetEscrow(escrowID)
	if err != nil {
		return fmt.Errorf("warm escrow %s: %w", escrowID, err)
	}
	if info.Settled {
		s.log.Debug("hostevents: skipping warm of settled escrow", "escrow_id", escrowID)
		return s.OnEscrowSettled(escrowID)
	}
	if err := s.store.PutEscrowCache(devshardbridge.EscrowCacheFromInfo(info)); err != nil {
		return fmt.Errorf("cache escrow %s: %w", escrowID, err)
	}
	s.log.Debug("hostevents: warmed escrow into cache", "escrow_id", escrowID, "epoch_id", info.EpochID)
	return nil
}

// OnEscrowSettled drops the warm cache row and finalizes any live session so a
// missed websocket event cannot leave a settled escrow serving inference.
func (s *escrowWarmSink) OnEscrowSettled(escrowID string) error {
	if err := s.store.DeleteEscrowCache(escrowID); err != nil {
		return fmt.Errorf("drop escrow cache %s: %w", escrowID, err)
	}
	s.log.Debug("hostevents: dropped settled escrow from cache", "escrow_id", escrowID)
	if s.onSettled == nil {
		return nil
	}
	if err := s.onSettled(escrowID); err != nil {
		return fmt.Errorf("finalize settled escrow %s: %w", escrowID, err)
	}
	return nil
}

// RehydrateOpenEscrows re-validates every locally-active session against the
// chain and finalizes the ones that already settled.
//
// needs_reset means the dapi cannot serve the host's cursor, so any settlement
// that happened in the gap is gone from the ring and will never be delivered as
// an event. Without this sweep those sessions keep serving inference the chain
// will refuse to pay for. It is also the backstop for a settlement whose
// dispatch failed past MaxDispatchAttempts: that skip advances the cursor, but
// the next reset re-derives the same conclusion from chain state.
//
// Warm cache rows are deliberately not rebuilt here: lazy bind still falls back
// to a live chain fetch, and subsequent escrow-created events refill them.
func (s *escrowWarmSink) RehydrateOpenEscrows() {
	if s.store == nil {
		return
	}
	if skip, since := s.throttleRehydrate(); skip {
		s.log.Debug("hostevents: skipping rehydrate sweep, ran recently", "since", since)
		return
	}

	active, err := s.store.ListActiveSessions()
	if err != nil {
		s.log.Warn("hostevents: rehydrate could not list active sessions", "error", err)
		s.clearRehydrateThrottle()
		return
	}

	deadline := time.Now().Add(rehydrateSweepBudget)
	var checked, finalized int
	complete := true
	for _, sess := range active {
		if time.Now().After(deadline) {
			s.log.Warn("hostevents: rehydrate budget exhausted, remaining escrows deferred",
				"checked", checked, "total", len(active))
			complete = false
			break
		}
		checked++
		settled, err := bridge.SettledWithin(s.bridge, sess.EscrowID, rehydrateEscrowCheckTimeout)
		if err != nil {
			// Fail open: a chain blip must not drop work this host bound.
			s.log.Warn("hostevents: rehydrate settled-check failed, leaving session active",
				"escrow_id", sess.EscrowID, "error", err)
			complete = false
			continue
		}
		if !settled {
			continue
		}
		if err := s.OnEscrowSettled(sess.EscrowID); err != nil {
			s.log.Error("hostevents: rehydrate failed to finalize settled escrow",
				"escrow_id", sess.EscrowID, "error", err)
			complete = false
			continue
		}
		finalized++
		s.log.Info("hostevents: rehydrate finalized escrow settled during the event gap",
			"escrow_id", sess.EscrowID)
	}

	if !complete {
		// Anything unresolved must not be held off by the throttle: let the
		// next reset retry immediately.
		s.clearRehydrateThrottle()
	}
	s.log.Info("hostevents: rehydrate sweep complete",
		"active", len(active), "checked", checked, "finalized", finalized, "complete", complete)
}

// throttleRehydrate reports whether this sweep should be skipped because
// another one started less than rehydrateMinInterval ago, and claims the window
// otherwise. A dapi stuck in a reset loop would otherwise re-query the chain for
// every open escrow on every poll; one sweep per interval is enough, since the
// answer only changes when a settlement lands and that also arrives as an event.
func (s *escrowWarmSink) throttleRehydrate() (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if !s.lastRehydrate.IsZero() {
		if since := now.Sub(s.lastRehydrate); since < rehydrateMinInterval {
			return true, since
		}
	}
	s.lastRehydrate = now
	return false, 0
}

func (s *escrowWarmSink) clearRehydrateThrottle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRehydrate = time.Time{}
}
