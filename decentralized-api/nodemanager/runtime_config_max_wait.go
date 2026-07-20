package nodemanager

import (
	"os"
	"strconv"
	"time"

	"common/runtimeconfig"
)

const defaultRuntimeConfigMaxWaitCap = runtimeconfig.DefaultMaxWaitCap

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
func clampMaxWait(maxWaitSeconds int32) time.Duration {
	return runtimeconfig.ClampMaxWait(maxWaitSeconds, runtimeConfigMaxWaitCap())
}
