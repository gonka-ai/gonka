package user

import "time"

// StepTimer receives how long one session step took; the gateway turns it into a histogram.
type StepTimer func(step string, elapsed time.Duration)

// The steps a StepTimer is called with.
const (
	StepLockWaitPrepare   = "lock_wait_prepare"
	StepLockHeldPrepare   = "lock_held_prepare"
	StepLockWaitProcess   = "lock_wait_process"
	StepLockHeldProcess   = "lock_held_process"
	StepStoreAppendDiff   = "store_append_diff"
	StepStoreAddSignature = "store_add_signature"
	StepHostCatchUp       = "host_catch_up"
)

// WithStepTimer times the session's lock, store and catch-up steps.
func WithStepTimer(timer StepTimer) SessionOption {
	return func(s *Session) { s.SetStepTimer(timer) }
}

// SetStepTimer times the steps of a session already built, as a recovered one is.
func (s *Session) SetStepTimer(timer StepTimer) {
	if timer != nil {
		s.stepTimer.Store(&timer)
	}
}

func (s *Session) timeStep(step string, started time.Time) {
	if timer := s.stepTimer.Load(); timer != nil {
		(*timer)(step, time.Since(started))
	}
}
