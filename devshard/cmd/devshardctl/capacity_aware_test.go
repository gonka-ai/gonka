package main

import "testing"

// setCapacityAwareLimitsForTest toggles the capacity-aware behavior
// flag for the duration of a test. Mirrors setPoCModeForTest so
// PoC + capacity-aware tests can compose freely.
func setCapacityAwareLimitsForTest(t *testing.T, on bool) {
	t.Helper()
	prev := capacityAwareLimitsState.Load()
	capacityAwareLimitsState.Store(on)
	t.Cleanup(func() {
		capacityAwareLimitsState.Store(prev)
	})
}
