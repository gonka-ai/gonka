package limits

import (
	"testing"

	"devshard/cmd/gateway/config"
)

// Test flow:
//  1. Build a limiter from `config.Defaults()` and price a request at the fallback context length and token cap.
//  2. Acquire requests in a loop until one is refused.
//  3. Assert a cold host admits exactly 32 requests before slow start narrows anything.
//  4. Assert the next request is refused with `AdmissionWindowFull`.
func TestTheDefaultWindowsAdmitAFullBurstBeforeTheyGrow(t *testing.T) {
	t.Parallel()
	settings := config.Defaults()
	limiter := newTestLimiter(ParticipantConfigFromLimits(settings.Limits), fixedNow(testEpoch))
	cost := TokenCost{
		Input:  settings.Limits.FallbackMaxModelLen,
		Output: settings.Limits.MaxTokensCap,
	}

	admitted := 0
	for range 32 {
		if _, admission := limiter.Acquire("participant-1", "model-a", cost); admission != AdmissionOpen {
			break
		}
		admitted++
	}

	if admitted != 32 {
		t.Fatalf("a cold host admitted %d requests at full price, want 32 before slow start has run at all", admitted)
	}
	if _, admission := limiter.Acquire("participant-1", "model-a", cost); admission != AdmissionWindowFull {
		t.Fatalf("the 33rd request was admitted as %q, want the window to hold the line", admission)
	}
}

// Test flow:
//  1. Build a limiter from `config.Defaults()` and beat its window down with 40 rounds of `MissedFirstTokenDeadline` and `DecodeStalled` results.
//  2. Acquire requests in a loop until one is refused.
//  3. Assert the host still admits the 16 requests its floor reserves, even after being beaten as hard as possible.
func TestTheWindowFloorKeepsAHostOffASingleRequest(t *testing.T) {
	t.Parallel()
	settings := config.Defaults()
	limiter := newTestLimiter(ParticipantConfigFromLimits(settings.Limits), fixedNow(testEpoch))
	cost := TokenCost{Input: settings.Limits.FallbackMaxModelLen, Output: settings.Limits.MaxTokensCap}

	for range 40 {
		limiter.OnResult(Result{
			Participant: "participant-1", Model: "model-a",
			Verdict: MissedFirstTokenDeadline, Carried: cost,
		})
		limiter.OnResult(Result{
			Participant: "participant-1", Model: "model-a",
			Verdict: DecodeStalled, Carried: cost,
		})
	}

	admitted := 0
	for range 16 {
		if _, admission := limiter.Acquire("participant-1", "model-a", cost); admission != AdmissionOpen {
			break
		}
		admitted++
	}

	if admitted != 16 {
		t.Fatalf("a host beaten to the floor admitted %d requests, want the 16 the floor reserves", admitted)
	}
}
