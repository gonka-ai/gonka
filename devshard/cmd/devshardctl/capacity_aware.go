package main

import (
	"sync/atomic"

	"devshard/internal/configenv"
)

// capacityAwareLimitsState gates the new capacity-aware behavior:
// when true the gateway and participant limiters drop their relaxed-PoC
// bypass and rely on CapacityState-driven scaled caps + reactive
// throttle instead. Default is false to preserve current behavior.
var capacityAwareLimitsState atomic.Bool

// ConfigureCapacityAwareLimits enables/disables capacity-aware limiter
// behavior based on a string value (env var, admin setting, etc.).
// Invalid and empty values disable the behavior.
func ConfigureCapacityAwareLimits(raw string) {
	enabled, err := configenv.ParseBool(raw)
	capacityAwareLimitsState.Store(err == nil && enabled)
}

// capacityAwareLimitsEnabled reports whether the gateway should keep
// enforcing rate limits during PoC (relying on CapacityState scaling)
// instead of bypassing them.
func capacityAwareLimitsEnabled() bool {
	return capacityAwareLimitsState.Load()
}
