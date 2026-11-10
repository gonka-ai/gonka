package limits

import (
	"testing"

	"devshard/cmd/gateway/config"
)

const (
	forcedEmptyHost          = "honest-host"
	forcedEmptyModel         = "model-a"
	forcedEmptyHostWeight    = 8_000
	forcedEmptyContextTokens = 1_000
	forcedEmptyEarnedFloor   = 4
	forcedEmptyFreshLoad     = 6
	forcedEmptyAnswers       = 2
)

// Test flow:
//  1. Build a limiter from the default limits, price the host by a chain weight that earns it 4 full-price requests on a 1000-token context, and read its fresh windows.
//  2. Hold 6 requests of 1000 input tokens at once and assert the fresh input window admits them all, then release them.
//  3. Post two EmptyAnswer results, the verdict an honest host still earns for a forced empty answer no second host in its race repeated.
//  4. Assert the input window fell to the earned floor of 4 requests while the output window, which a weighted host starts at its floor, did not move.
//  5. Hold 4 such requests and assert the fifth is refused as window_full, the admission the scheduler burns as ghostWindowFull.
func TestTwoForcedEmptyAnswersCollapseAWeightedHostToItsEarnedFloor(t *testing.T) {
	limiter := newTestLimiter(ParticipantConfigFromLimits(config.Defaults().Limits), fixedNow(testEpoch))
	limiter.ObserveWeights(map[string]map[string]float64{forcedEmptyModel: {forcedEmptyHost: forcedEmptyHostWeight}})
	limiter.ObserveModels(map[string]int64{forcedEmptyModel: forcedEmptyContextTokens})
	request := TokenCost{Input: forcedEmptyContextTokens, Output: 1}
	limiter.acquiredAndReleased(t, forcedEmptyHost, forcedEmptyModel)
	freshInput, freshOutput := limiter.windowsOf(t, forcedEmptyHost, forcedEmptyModel)
	if freshInput != initialInputWindowFloorMultiple*forcedEmptyEarnedFloor*forcedEmptyContextTokens {
		t.Fatalf("fresh input window = %v, want twice the earned floor", freshInput)
	}

	releases := make([]func(), 0, forcedEmptyFreshLoad)
	for held := range forcedEmptyFreshLoad {
		release, admission := limiter.Acquire(forcedEmptyHost, forcedEmptyModel, request)
		if admission != AdmissionOpen {
			t.Fatalf("fresh window refused request %d with %q, want it admitted", held+1, admission)
		}
		releases = append(releases, release)
	}
	for _, release := range releases {
		release()
	}

	for range forcedEmptyAnswers {
		limiter.OnResult(Result{Participant: forcedEmptyHost, Model: forcedEmptyModel, Verdict: EmptyAnswer, Carried: request})
	}

	input, output := limiter.windowsOf(t, forcedEmptyHost, forcedEmptyModel)
	if input != forcedEmptyEarnedFloor*forcedEmptyContextTokens {
		t.Errorf("input window after two empty answers = %v, want the earned floor %d", input, forcedEmptyEarnedFloor*forcedEmptyContextTokens)
	}
	if output != freshOutput {
		t.Errorf("output window after two empty answers = %v, want it left at its floor %v", output, freshOutput)
	}
	for held := range forcedEmptyEarnedFloor {
		if _, admission := limiter.Acquire(forcedEmptyHost, forcedEmptyModel, request); admission != AdmissionOpen {
			t.Fatalf("collapsed window refused request %d with %q, want the earned floor still admitted", held+1, admission)
		}
	}
	if _, admission := limiter.Acquire(forcedEmptyHost, forcedEmptyModel, request); admission != AdmissionWindowFull {
		t.Fatalf("request past the earned floor admission = %q, want %q", admission, AdmissionWindowFull)
	}
}
