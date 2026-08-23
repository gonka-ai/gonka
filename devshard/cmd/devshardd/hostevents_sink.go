package main

import (
	"fmt"
	"log/slog"

	"devshard/bridge"
	devshardbridge "devshard/cmd/devshardd/bridge"
	devshardstorage "devshard/storage"
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

// RehydrateOpenEscrows is a no-op: warm rows are re-populated by subsequent
// escrow-created events after a needs_reset, and lazy bind still falls back to a
// live chain fetch, so there is nothing to rebuild eagerly here.
func (s *escrowWarmSink) RehydrateOpenEscrows() {
	s.log.Debug("hostevents: rehydrate requested (no-op; lazy bind + future events refill cache)")
}
