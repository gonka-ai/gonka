package admin

import (
	"context"
	"decentralized-api/logging"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

// StartOnboardingStatusLogger periodically emits a clear, operator-facing
// "waiting for PoC" status line while the participant is onboarding
// (configured but not yet in the active set). This makes the most
// visible log during the wait the onboarding status — addressing the
// "clearer logging when a node is launched and waiting for PoC" goal —
// instead of leaving operators to guess. It logs once immediately, then
// on each tick, and goes quiet once the participant is active (logging
// the inactive->active transition exactly once). Reads cached state
// only; performs no chain RPCs and does not touch the broker.
func (s *Server) StartOnboardingStatusLogger(ctx context.Context, interval time.Duration) {
	if s.phaseTracker == nil {
		return
	}
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		active := s.logOnboardingStatus(false)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				active = s.logOnboardingStatus(active)
			}
		}
	}()
}

// logOnboardingStatus logs the current onboarding/waiting status once and
// returns whether the participant is currently active. prevActive lets it
// announce the inactive->active transition exactly once and then stay
// quiet while active. No-op (returns prevActive) when no MLnode is
// configured, since there is nothing to wait for.
func (s *Server) logOnboardingStatus(prevActive bool) bool {
	if len(s.configManager.GetNodes()) == 0 {
		return prevActive
	}
	active := s.activityTracker != nil && s.activityTracker.IsActive()
	if active {
		if !prevActive {
			logging.Info("Participant is now in the active set and participating", types.Participants)
		}
		return true
	}
	seconds := SecondsUntilPoCUnknown
	if timing := ComputeTiming(s.phaseTracker.GetCurrentEpochState()); timing != nil {
		seconds = timing.SecondsUntilNextPoC
	}
	logging.Info(BuildMLNodeMessage(MLNodeState_WAITING_FOR_POC, seconds, ""), types.Nodes)
	return false
}
