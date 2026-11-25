package limits

import (
	"testing"
)

func inflightOutput(l *ParticipantLimiter, participant, model string) int64 {
	for _, window := range l.Snapshot() {
		if window.Participant == participant && window.Model == model {
			return window.InflightOutputTokens
		}
	}
	return -1
}

// Test flow:
//  1. Build a limiter and call `Overdraft` with a cost of 1024 input and 4096 output tokens.
//  2. Assert the admission is open and both in-flight input and output equal what was charged.
//  3. Release the lease (twice, to check idempotency) and assert both in-flight counts return to 0.
func TestALeaseGivesBackExactlyWhatItTook(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(testConfig(), fixedNow(testEpoch))
	cost := TokenCost{Input: 1_024, Output: 4_096}

	release, admission := limiter.Overdraft("participant-1", "model-a", cost)

	if admission != AdmissionOpen {
		t.Fatalf("Overdraft = %q, want the tokens taken", admission)
	}
	if got := inflightInput(limiter, "participant-1", "model-a"); got != cost.Input {
		t.Fatalf("in-flight input = %d, want the %d it was charged", got, cost.Input)
	}
	if got := inflightOutput(limiter, "participant-1", "model-a"); got != cost.Output {
		t.Fatalf("in-flight output = %d, want the %d it was charged", got, cost.Output)
	}

	release()
	release()

	if got := inflightInput(limiter, "participant-1", "model-a"); got != 0 {
		t.Fatalf("in-flight input = %d after release, want 0", got)
	}
	if got := inflightOutput(limiter, "participant-1", "model-a"); got != 0 {
		t.Fatalf("in-flight output = %d after release, want 0", got)
	}
}

// Test flow:
//  1. Build a limiter and call `Overdraft` with a small charged cost, then release it.
//  2. Report a successful result carrying the same charged cost.
//  3. Assert the output window grew, crediting the reservation the window was charged rather than what the answer produced.
func TestAWindowGrowsInTheCurrencyItCharges(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(testConfig(), fixedNow(testEpoch))
	charged := TokenCost{Input: 2, Output: 2}

	release, admission := limiter.Overdraft("participant-1", "model-a", charged)
	if admission != AdmissionOpen {
		t.Fatalf("Overdraft = %q, want the tokens taken", admission)
	}
	before := outputWindow(t, limiter, "participant-1", "model-a")
	release()
	limiter.OnResult(Result{Participant: "participant-1", Model: "model-a", Verdict: Success, Carried: charged})
	grown := outputWindow(t, limiter, "participant-1", "model-a")

	if grown <= before {
		t.Fatalf("output window %v -> %v, want a rung earned by what the window was charged", before, grown)
	}
}

func outputWindow(t *testing.T, l *ParticipantLimiter, participant, model string) float64 {
	t.Helper()
	for _, window := range l.Snapshot() {
		if window.Participant == participant && window.Model == model {
			return window.OutputWindowTokens
		}
	}
	t.Fatalf("no window for %s/%s", participant, model)
	return 0
}
