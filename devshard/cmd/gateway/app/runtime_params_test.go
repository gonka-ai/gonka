package app

import "testing"

// Test flow:
//  1. Hold a nil *runtimeParams.
//  2. Call Heartbeat on it and assert it returns nil.
//  3. Call Close on it and assert it returns nil.
func TestAnUnbuiltParamsFeedAnswersAndClosesWithoutPanicking(t *testing.T) {
	var absent *runtimeParams

	if schedule := absent.Heartbeat(); schedule != nil {
		t.Fatalf("Heartbeat() = %+v, want nil so the session keeps its compiled schedule", *schedule)
	}
	if err := absent.Close(); err != nil {
		t.Fatalf("Close() = %v, want a feed that was never opened to close cleanly", err)
	}
}
