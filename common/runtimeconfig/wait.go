package runtimeconfig

import (
	"context"
	"time"
)

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
