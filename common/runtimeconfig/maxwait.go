package runtimeconfig

import "time"

// DefaultMaxWaitCap is the server-side upper bound applied to positive
// max_wait_seconds when a caller does not supply its own cap.
const DefaultMaxWaitCap = 60 * time.Second

// ClampMaxWait maps a client max_wait_seconds to an effective hold duration.
//
// Wire contract (do NOT reinterpret 0):
//   - <= 0: immediate reply (field absent decodes as 0)
//   - > 0:  long-poll up to min(requested, cap)
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
