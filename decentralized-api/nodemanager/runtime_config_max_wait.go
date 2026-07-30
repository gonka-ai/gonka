package nodemanager

import (
	"os"
	"strconv"
	"time"

	"decentralized-api/internal/longpoll"
)

const defaultRuntimeConfigMaxWaitCap = longpoll.DefaultMaxWaitCap

// runtimeConfigMaxWaitCap is the server-side upper bound for GetRuntimeConfig
// positive max_wait_seconds (overridable via DAPI_RUNTIME_CONFIG_MAX_WAIT_SECONDS).
func runtimeConfigMaxWaitCap() time.Duration {
	if v := os.Getenv("DAPI_RUNTIME_CONFIG_MAX_WAIT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultRuntimeConfigMaxWaitCap
}

// clampMaxWait maps client max_wait_seconds for GetRuntimeConfig.
func clampMaxWait(maxWaitSeconds int32) time.Duration {
	return longpoll.ClampMaxWait(maxWaitSeconds, runtimeConfigMaxWaitCap())
}

// hostEventsMaxWaitCap shares the runtime-config env override when set.
func hostEventsMaxWaitCap() time.Duration {
	return runtimeConfigMaxWaitCap()
}
