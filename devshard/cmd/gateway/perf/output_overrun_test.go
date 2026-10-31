package perf

import "testing"

// Test flow:
//  1. Build a tracker.
//  2. Record two output overruns for participant-a on one model and one on another model.
//  3. Assert `OutputOverruns` counts each host and model on its own, and nothing for an unrecorded participant.
func TestTrackerCountsOutputOverrunsPerHostAndModel(t *testing.T) {
	tracker := newTestTracker(testPerf(), fixedNow(testEpoch))

	tracker.RecordOutputOverrun("participant-a", capabilityModel)
	tracker.RecordOutputOverrun("participant-a", capabilityModel)
	tracker.RecordOutputOverrun("participant-a", "model-b")

	if got := tracker.OutputOverruns("participant-a", capabilityModel); got != 2 {
		t.Errorf("overruns on %s = %d, want 2", capabilityModel, got)
	}
	if got := tracker.OutputOverruns("participant-a", "model-b"); got != 1 {
		t.Errorf("overruns on model-b = %d, want 1", got)
	}
	if got := tracker.OutputOverruns("participant-b", capabilityModel); got != 0 {
		t.Errorf("an unknown participant reports %d overruns, want 0", got)
	}
}
