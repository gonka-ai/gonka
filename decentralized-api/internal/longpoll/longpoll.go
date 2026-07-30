// Package longpoll holds small helpers shared by NodeManager long-poll RPCs
// (GetRuntimeConfig, GetHostEvents).
package longpoll

import (
	"context"
	"time"
)

// DefaultMaxWaitCap is the server-side upper bound when no override is supplied.
const DefaultMaxWaitCap = 60 * time.Second

// ClampMaxWait maps client max_wait_seconds to an effective hold duration.
//
// Wire contract (shared by GetRuntimeConfig / GetHostEvents):
//   - <= 0: immediate reply (field absent / default decodes as 0)
//   - > 0: long-poll up to min(requested, cap); cap <= 0 uses DefaultMaxWaitCap
func ClampMaxWait(maxWaitSeconds int32, cap time.Duration) time.Duration {
	if maxWaitSeconds <= 0 {
		return 0
	}
	if cap <= 0 {
		cap = DefaultMaxWaitCap
	}
	requested := time.Duration(maxWaitSeconds) * time.Second
	if requested > cap {
		return cap
	}
	return requested
}

// Outcome is how Wait returned.
type Outcome int

const (
	// Notified means the wake channel closed.
	Notified Outcome = iota
	// TimedOut means maxWait elapsed with no wake.
	TimedOut
)

// Wait blocks until wake closes, maxWait elapses, or ctx is done.
// maxWait must be > 0; callers that want an immediate reply should not call Wait.
// On context cancel, returns (_, ctx.Err()).
func Wait(ctx context.Context, wake <-chan struct{}, maxWait time.Duration) (Outcome, error) {
	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	select {
	case <-wake:
		return Notified, nil
	case <-timer.C:
		return TimedOut, nil
	case <-ctx.Done():
		return TimedOut, ctx.Err()
	}
}
