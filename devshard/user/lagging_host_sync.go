package user

import (
	"context"

	"devshard/logging"
)

const laggingHostSyncNonces = maxRetainedDiffs

func (s *Session) syncLaggingHostsLocked() {
	if s.laggingSyncClosed {
		return
	}
	if s.laggingSyncAt == nil {
		s.laggingSyncAt = make(map[int]uint64)
	}
	for hostIdx := range s.clients {
		if s.nonce-s.hostSyncNonce[hostIdx] < laggingHostSyncNonces || s.nonce-s.laggingSyncAt[hostIdx] < laggingHostSyncNonces {
			continue
		}
		s.laggingSyncAt[hostIdx] = s.nonce
		if s.laggingSyncCtx == nil {
			s.laggingSyncCtx, s.laggingSyncCancel = context.WithCancel(context.Background())
		}
		s.laggingSyncs.Add(1)
		go s.syncLaggingHost(s.laggingSyncCtx, hostIdx)
	}
}

func (s *Session) syncLaggingHost(ctx context.Context, hostIdx int) {
	defer s.laggingSyncs.Done()
	hostMu := s.hostCatchUpLock(hostIdx)
	if !hostMu.TryLock() {
		return
	}
	defer hostMu.Unlock()
	s.mu.Lock()
	through := s.nonce
	s.mu.Unlock()
	if err := s.sendCatchUpThrough(ctx, hostIdx, s.getFinalizeClients()[hostIdx], through); err != nil {
		logging.Debug("lagging host sync did not reach the host", "subsystem", "session",
			"escrow", s.escrowID, "host", hostIdx, "through", through, "error", err)
	}
}

func (s *Session) stopLaggingSync() {
	s.mu.Lock()
	s.laggingSyncClosed = true
	if s.laggingSyncCancel != nil {
		s.laggingSyncCancel()
	}
	s.mu.Unlock()
	s.laggingSyncs.Wait()
}
