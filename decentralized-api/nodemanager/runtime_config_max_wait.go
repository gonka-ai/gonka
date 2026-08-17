package nodemanager

import (
	"os"
	"strconv"
	"time"

	"common/runtimeconfig"
)

const defaultRuntimeConfigMaxWaitCap = runtimeconfig.DefaultMaxWaitCap

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

// clampMaxWait maps client max_wait_seconds to an effective hold duration.
// Kept for GetRuntimeConfig's MaxWaitCap wiring (see runtime_config_adapter.go)
// and tested directly; GetRuntimeConfig itself delegates the long-poll loop to
// common/runtimeconfig.Server, which applies the same clamp internally.
func clampMaxWait(maxWaitSeconds int32) time.Duration {
	return runtimeconfig.ClampMaxWait(maxWaitSeconds, runtimeConfigMaxWaitCap())
}

// hostEventsMaxWaitCap shares the runtime-config env override when set.
// clampHostEventsMaxWait (host_events_rpc.go) applies it via longpoll.ClampMaxWait.
func hostEventsMaxWaitCap() time.Duration {
	return runtimeConfigMaxWaitCap()
}
