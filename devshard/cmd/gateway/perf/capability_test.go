package perf

import (
	"fmt"
	"sync"
	"testing"
)

const capabilityModel = "model-a"

// Test flow:
//  1. Build a capability tracker.
//  2. Record a context limit of 4096 for host-0/model-a.
//  3. Assert the tracker reports that limit and one context refusal.
func TestARecordedContextLimitIsWhatTheHostAdmittedTo(t *testing.T) {
	t.Parallel()
	tracker := newCapabilityTracker()

	tracker.recordContextLimit("host-0", capabilityModel, 4096)

	limit, _, _, refusals := tracker.capability("host-0", capabilityModel)
	if limit != 4096 || refusals != 1 {
		t.Errorf("limit/refusals = %d/%d, want 4096 and one refusal", limit, refusals)
	}
}

// Test flow:
//  1. Build a capability tracker.
//  2. Record a context limit of 0 for host-0/model-a.
//  3. Assert nothing was recorded: the limit and refusal count both stay zero.
func TestAZeroContextLimitIsNotRecorded(t *testing.T) {
	t.Parallel()
	tracker := newCapabilityTracker()

	tracker.recordContextLimit("host-0", capabilityModel, 0)

	limit, _, _, refusals := tracker.capability("host-0", capabilityModel)
	if limit != 0 || refusals != 0 {
		t.Errorf("limit/refusals = %d/%d, want nothing recorded", limit, refusals)
	}
}

// Test flow:
//  1. Build a capability tracker.
//  2. Record a context limit of 8192, then a tighter 2048, for host-0/model-a.
//  3. Assert the tracker reports the newer 2048 limit with both refusals counted.
func TestTheLatestContextLimitReplacesTheOneBefore(t *testing.T) {
	t.Parallel()
	tracker := newCapabilityTracker()

	tracker.recordContextLimit("host-0", capabilityModel, 8192)
	tracker.recordContextLimit("host-0", capabilityModel, 2048)

	limit, _, _, refusals := tracker.capability("host-0", capabilityModel)
	if limit != 2048 || refusals != 2 {
		t.Errorf("limit/refusals = %d/%d, want the newer 2048 and both refusals counted", limit, refusals)
	}
}

// Test flow:
//  1. Build a capability tracker.
//  2. Record two version-unsupported refusals and one tool-unsupported refusal for host-0.
//  3. Assert the tracker reports 2 version refusals and 1 tool refusal for model-a.
func TestRefusalsAreCountedRatherThanJudged(t *testing.T) {
	t.Parallel()
	tracker := newCapabilityTracker()

	tracker.recordVersionUnsupported("host-0")
	tracker.recordVersionUnsupported("host-0")
	tracker.recordToolUnsupported("host-0", capabilityModel)

	_, versionRefusals, toolRefusals, _ := tracker.capability("host-0", capabilityModel)
	if versionRefusals != 2 || toolRefusals != 1 {
		t.Errorf("version/tool refusals = %d/%d, want 2 and 1", versionRefusals, toolRefusals)
	}
}

// Test flow:
//  1. Build a capability tracker.
//  2. Record a tool-unsupported refusal and a context limit for host-0/model-a, and a version-unsupported refusal for host-0.
//  3. Assert querying host-0/model-b reports no context limit and no tool or context refusals.
//  4. Assert host-0/model-b still reports the build-level version refusal.
func TestAModelsRefusalIsNotReportedAgainstAnotherModel(t *testing.T) {
	t.Parallel()
	tracker := newCapabilityTracker()

	tracker.recordToolUnsupported("host-0", capabilityModel)
	tracker.recordContextLimit("host-0", capabilityModel, 4096)
	tracker.recordVersionUnsupported("host-0")

	limit, versionRefusals, toolRefusals, contextRefusals := tracker.capability("host-0", "model-b")
	if limit != 0 || toolRefusals != 0 || contextRefusals != 0 {
		t.Errorf("model-b reports limit %d, %d tool and %d context refusals, want none",
			limit, toolRefusals, contextRefusals)
	}
	if versionRefusals != 1 {
		t.Errorf("model-b reports %d version refusals, want the build's own 1", versionRefusals)
	}
}

// Test flow:
//  1. Build a capability tracker.
//  2. For each of 8 participants, launch concurrent goroutines that record context limits, record tool-unsupported refusals, and read capability, 200 iterations each.
//  3. Wait for all goroutines to finish.
//  4. Assert each participant's tool-refusal count equals the iteration count.
func TestCapabilityTrackerConcurrentAccessIsRaceFree(t *testing.T) {
	tracker := newCapabilityTracker()
	const participantCount = 8
	const iterations = 200

	var waiting sync.WaitGroup
	for index := range participantCount {
		participant := fmt.Sprintf("participant-%d", index)
		waiting.Add(3)
		go func() {
			defer waiting.Done()
			for iteration := range iterations {
				tracker.recordContextLimit(participant, capabilityModel, uint64(1000+iteration))
			}
		}()
		go func() {
			defer waiting.Done()
			for range iterations {
				tracker.recordToolUnsupported(participant, capabilityModel)
			}
		}()
		go func() {
			defer waiting.Done()
			for range iterations {
				_, _, _, _ = tracker.capability(participant, capabilityModel)
			}
		}()
	}
	waiting.Wait()

	for index := range participantCount {
		participant := fmt.Sprintf("participant-%d", index)
		if _, _, toolRefusals, _ := tracker.capability(participant, capabilityModel); toolRefusals != iterations {
			t.Fatalf("%s counted %d tool refusals, want %d", participant, toolRefusals, iterations)
		}
	}
}

// Test flow:
//  1. Build a capability tracker.
//  2. Record a context limit of 16000, then a larger 32000, for host-a/model-a.
//  3. Assert the second record reports no change and returns the earlier 16000 as the previous value.
//  4. Assert the tracker still reports the smaller 16000 limit with both refusals counted.
func TestTheReportedContextLimitIsTheSmallestRefusal(t *testing.T) {
	tracker := newCapabilityTracker()

	tracker.recordContextLimit("host-a", "model-a", 16_000)
	previous, changed := tracker.recordContextLimit("host-a", "model-a", 32_000)

	if changed {
		t.Errorf("a larger refusal reported a change from %d: it does not lift the smaller bound", previous)
	}
	limit, _, _, refusals := tracker.capability("host-a", "model-a")
	if limit != 16_000 {
		t.Errorf("context limit = %d, want the smallest refusal 16000", limit)
	}
	if refusals != 2 {
		t.Errorf("context refusals = %d, want both counted", refusals)
	}
}
