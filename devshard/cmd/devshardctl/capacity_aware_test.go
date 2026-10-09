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

func TestConfigureCapacityAwareLimitsUsesDevshardBooleanGrammar(t *testing.T) {
	setCapacityAwareLimitsForTest(t, false)

	ConfigureCapacityAwareLimits("yes")
	if !capacityAwareLimitsEnabled() {
		t.Fatal("yes must enable capacity-aware limits")
	}

	ConfigureCapacityAwareLimits("f")
	if capacityAwareLimitsEnabled() {
		t.Fatal("f must disable capacity-aware limits")
	}

	ConfigureCapacityAwareLimits("invalid")
	if capacityAwareLimitsEnabled() {
		t.Fatal("invalid value must retain the disabled fallback")
	}
}
