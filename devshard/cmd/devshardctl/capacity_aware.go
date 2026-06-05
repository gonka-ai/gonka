package main

import (
	"strings"
	"sync/atomic"
)

// capacityAwareLimitsState gates the new capacity-aware behavior:
// when true the gateway and participant limiters drop their relaxed-PoC
// bypass and rely on CapacityState-driven scaled caps + reactive
// throttle instead. Default is false to preserve current behavior.
var capacityAwareLimitsState atomic.Bool

// ConfigureCapacityAwareLimits enables/disables capacity-aware limiter
// behavior based on a string value (env var, admin setting, etc.).
// Recognized truthy values: "1", "true", "on", "yes"; everything else
// (including empty) disables it.
func ConfigureCapacityAwareLimits(raw string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "on", "yes":
		capacityAwareLimitsState.Store(true)
	default:
		capacityAwareLimitsState.Store(false)
	}
}

// capacityAwareLimitsEnabled reports whether the gateway should keep
// enforcing rate limits during PoC (relying on CapacityState scaling)
// instead of bypassing them.
func capacityAwareLimitsEnabled() bool {
	return capacityAwareLimitsState.Load()
}
