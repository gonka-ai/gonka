package nodemanager

import (
	"os"
	"strconv"
	"time"
)

const defaultRuntimeConfigMaxWaitCap = 60 * time.Second

// runtimeConfigMaxWaitCap is the server-side upper bound for positive max_wait_seconds.
func runtimeConfigMaxWaitCap() time.Duration {
	if v := os.Getenv("DAPI_RUNTIME_CONFIG_MAX_WAIT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultRuntimeConfigMaxWaitCap
}

// clampMaxWait maps client max_wait_seconds to an effective hold duration.
//
// Wire contract:
//   - <= 0: immediate reply (3a contract; field absent decodes as 0)
//   - > 0: long-poll up to min(requested, server cap)
func clampMaxWait(maxWaitSeconds int32) time.Duration {
	if maxWaitSeconds <= 0 {
		return 0
	}
	maxWaitCap := runtimeConfigMaxWaitCap()
	requested := time.Duration(maxWaitSeconds) * time.Second
	if requested > maxWaitCap {
		return maxWaitCap
	}
	return requested
}
