package limits

import (
	"testing"

	"devshard/cmd/gateway/config"
)

const earnedModel = "model-a"

var earnedEpoch = testEpoch

// weightedConfig returns a ParticipantConfig with pricing constants pinned independently of the defaults.
func weightedConfig() ParticipantConfig {
	settings := ParticipantConfigFromLimits(config.Defaults().Limits)
	settings.Pricing.ConcurrencyPer10000Weight = 8
	settings.Pricing.ContextTokensByModel = map[string]int64{earnedModel: 40_000}
	settings.Pricing.OutputTokensByModel = map[string]int64{earnedModel: 4_096}
	return settings
}

func concurrentRequests(t *testing.T, limiter *ParticipantLimiter, participant string) int {
	t.Helper()
	cost := TokenCost{Input: 40_000, Output: 4_096}
	admitted := 0
	for range 256 {
		if _, admission := limiter.Acquire(participant, earnedModel, cost); admission != AdmissionOpen {
			break
		}
		admitted++
	}
	return admitted
}

// Test flow:
//  1. Build a limiter from `weightedConfig` and observe weights of 40000 for "strong" and 10000 for "weak".
//  2. Drive concurrent requests for both hosts via `concurrentRequests`.
//  3. Assert "weak" admits 8 requests and "strong" admits 32, matching what each weight buys.
func TestAHostsWindowIsSizedByTheWeightItEarned(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(weightedConfig(), fixedNow(earnedEpoch))
	limiter.ObserveWeights(map[string]map[string]float64{earnedModel: {
		"strong": 40_000,
		"weak":   10_000,
	}})

	strong, weak := concurrentRequests(t, limiter, "strong"), concurrentRequests(t, limiter, "weak")

	if weak != 8 {
		t.Fatalf("the 10000-weight host took %d requests, want the 8 its weight buys", weak)
	}
	if strong != 32 {
		t.Fatalf("the 40000-weight host took %d requests, want the 32 its weight buys", strong)
	}
}

// Test flow:
//  1. Build a limiter from `weightedConfig` without observing any weights.
//  2. Drive concurrent requests for an "unweighed" host via `concurrentRequests`.
//  3. Assert it admits the configured 32 requests.
func TestAHostWithNoWeightKeepsTheConfiguredWindow(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(weightedConfig(), fixedNow(earnedEpoch))

	if admitted := concurrentRequests(t, limiter, "unweighed"); admitted != 32 {
		t.Fatalf("an unweighed host took %d requests, want the configured 32", admitted)
	}
}

// Test flow:
//  1. Build a limiter and observe a weight of 40000 for "host", then confirm `concurrentRequests` admits 32.
//  2. Observe the weight drop to 10000 for the same host.
//  3. Assert `WindowFor` still tracks the host and its output window shrank to the 8 requests the new weight buys.
func TestAWindowFollowsTheWeightDown(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(weightedConfig(), fixedNow(earnedEpoch))
	limiter.ObserveWeights(map[string]map[string]float64{earnedModel: {"host": 40_000}})
	if admitted := concurrentRequests(t, limiter, "host"); admitted != 32 {
		t.Fatalf("the host took %d requests at full weight, want 32", admitted)
	}

	limiter.ObserveWeights(map[string]map[string]float64{earnedModel: {"host": 10_000}})

	window, tracked := limiter.WindowFor("host", earnedModel)
	if !tracked {
		t.Fatal("the host stopped being tracked when its weight moved")
	}
	if window.OutputWindowTokens != 8*4_096 {
		t.Fatalf("output window = %v tokens, want the 8 requests the smaller weight buys", window.OutputWindowTokens)
	}
}

// Test flow:
//  1. Build a limiter and observe a weight of 10000 for "host".
//  2. Admit one request for the host and read its `bounds` from the limiter's internal state.
//  3. Assert the output window's floor and start both equal the 8 requests the weight buys.
//  4. Assert the input window floors at 8 contexts and starts at twice that, and each window's step is one request's cost.
func TestTheTwoWindowsArePricedOnWhatARequestCosts(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(weightedConfig(), fixedNow(earnedEpoch))
	limiter.ObserveWeights(map[string]map[string]float64{earnedModel: {"host": 10_000}})

	limiter.admitOne("host", earnedModel)
	bounds := limiter.states[key{participant: "host", model: earnedModel}].bounds

	const concurrency = 8
	if bounds.Output.Min != concurrency*4_096 || bounds.Output.Initial != concurrency*4_096 {
		t.Fatalf("output window = %+v, want floor and start both at the %d requests 10000 weight buys",
			bounds.Output, concurrency)
	}
	if bounds.Input.Min != concurrency*40_000 || bounds.Input.Initial != 2*concurrency*40_000 {
		t.Fatalf("input window = %+v, want a floor of %d contexts and a start of twice that",
			bounds.Input, concurrency)
	}
	if bounds.Input.Step != 40_000 || bounds.Output.Step != 4_096 {
		t.Fatalf("steps = (%d, %d), want one context and one output budget: an answer buys a whole request",
			bounds.Input.Step, bounds.Output.Step)
	}
}

// Test flow:
//  1. Build a limiter and observe a weight of 100, too small to buy a whole request, for "tiny".
//  2. Drive concurrent requests for "tiny" via `concurrentRequests`.
//  3. Assert it still admits exactly 1 request.
func TestTheSmallestWeightStillBuysOneRequest(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(weightedConfig(), fixedNow(earnedEpoch))
	limiter.ObserveWeights(map[string]map[string]float64{earnedModel: {"tiny": 100}})

	if admitted := concurrentRequests(t, limiter, "tiny"); admitted != 1 {
		t.Fatalf("a 100-weight host took %d requests, want exactly 1", admitted)
	}
}

// Test flow:
//  1. Build a limiter and call `WindowFor` for a host it has never admitted.
//  2. Assert the pair is not tracked.
//  3. Admit one request for "host" and call `WindowFor` again.
//  4. Assert the pair is now tracked.
func TestWindowForAnswersOnlyForATrackedPair(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(weightedConfig(), fixedNow(earnedEpoch))

	if _, tracked := limiter.WindowFor("never-seen", earnedModel); tracked {
		t.Fatal("WindowFor invented a window for a host the limiter has never admitted")
	}
	limiter.admitOne("host", earnedModel)
	if _, tracked := limiter.WindowFor("host", earnedModel); !tracked {
		t.Fatal("WindowFor lost a host the limiter is tracking")
	}
}

// Test flow:
//  1. Build a `ModelCapacity` with a weight that buys 5 concurrent requests and call `effectiveConcurrencyLimit`.
//  2. Assert the limit is capped at the 5 the weight buys.
//  3. Grow the same capacity's `HostWindowRequests` to 40 and call `effectiveConcurrencyLimit` again.
//  4. Assert the limit follows the grown window up to 40.
func TestTheFrontDoorFollowsWhatTheHostsEarned(t *testing.T) {
	t.Parallel()
	weightDerived := ModelCapacity{CurrentWeight: 10_000, BaselineWeight: 10_000, MaxConcurrentPer10000Weight: 5}

	cold, limited := effectiveConcurrencyLimit(2048, weightDerived)
	if !limited || cold != 5 {
		t.Fatalf("cold fleet limit = %d (limited %v), want the 5 its weight buys", cold, limited)
	}

	grown := weightDerived
	grown.HostWindowRequests = 40
	if limit, _ := effectiveConcurrencyLimit(2048, grown); limit != 40 {
		t.Fatalf("fleet limit = %d once the hosts grew to 40, want 40: the door must not shut out what they earned", limit)
	}
}

// Test flow:
//  1. Call `effectiveConcurrencyLimit` with a `ModelCapacity` that has host windows but no weight data.
//  2. Assert the limit equals the 24 requests the host windows allow.
func TestTheFrontDoorTakesTheHostWindowsWithoutAWeight(t *testing.T) {
	t.Parallel()

	limit, limited := effectiveConcurrencyLimit(2048, ModelCapacity{HostWindowRequests: 24})

	if !limited || limit != 24 {
		t.Fatalf("fleet limit = %d (limited %v), want the 24 the hosts can take", limit, limited)
	}
}

// Test flow:
//  1. Build a limiter, observe weights for hosts "a" and "b", and admit one request each on `earnedModel`, plus one for host "c" on a different model.
//  2. Assert `ModelConcurrency` for `earnedModel` equals the sum of what "a" and "b" earned, excluding "c".
//  3. Assert `ModelConcurrency` for a model no host serves is 0.
func TestTheFleetsRoomIsTheSumOfItsHostsWindows(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(weightedConfig(), fixedNow(earnedEpoch))
	limiter.ObserveWeights(map[string]map[string]float64{earnedModel: {"a": 10_000, "b": 20_000}})
	limiter.admitOne("a", earnedModel)
	limiter.admitOne("b", earnedModel)
	limiter.admitOne("c", "other-model")

	if room := limiter.ModelConcurrency(earnedModel); room != 8+16 {
		t.Fatalf("room for %s = %d, want the 8 and 16 its two hosts buy, and nothing from another model", earnedModel, room)
	}
	if room := limiter.ModelConcurrency("never-served"); room != 0 {
		t.Fatalf("room for a model no host serves = %d, want 0", room)
	}
}

// Test flow:
//  1. Build a `ModelCapacity` whose host windows grew to 5000 requests.
//  2. Call `effectiveConcurrencyLimit` with a configured maximum of 2048 and assert the limit is capped there.
//  3. Call it again with no configured maximum (0) and assert the limit follows the hosts up to 5000.
func TestTheConfiguredMaximumStillCapsADoorTheHostsLifted(t *testing.T) {
	t.Parallel()
	grown := ModelCapacity{CurrentWeight: 10_000, BaselineWeight: 10_000, MaxConcurrentPer10000Weight: 5, HostWindowRequests: 5_000}

	if limit, _ := effectiveConcurrencyLimit(2048, grown); limit != 2048 {
		t.Fatalf("fleet limit = %d, want the configured 2048: a window that grew past the process's own ceiling is not admission", limit)
	}
	if limit, _ := effectiveConcurrencyLimit(0, grown); limit != 5_000 {
		t.Fatalf("fleet limit = %d with no configured maximum, want the 5000 the hosts can take", limit)
	}
}

// Test flow:
//  1. Build a limiter, observe a weight of 100 for "host", and acquire one request.
//  2. Release it and report a successful result, growing the output window to 2 requests.
//  3. Observe the same weight snapshot again, unchanged.
//  4. Assert the output window still reflects what the host earned, not reverted by the snapshot.
func TestAGrownWindowSurvivesAWeightSnapshotThatChangedNothing(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(weightedConfig(), fixedNow(earnedEpoch))
	limiter.ObserveWeights(map[string]map[string]float64{earnedModel: {"host": 100}})

	cost := TokenCost{Input: 40_000, Output: 4_096}
	release, admission := limiter.Acquire("host", earnedModel, cost)
	if admission != AdmissionOpen {
		t.Fatalf("the host refused its first request with %v", admission)
	}
	release()
	limiter.OnResult(Result{Participant: "host", Model: earnedModel, Verdict: Success, Carried: cost})

	_, grown := limiter.windowsOf(t, "host", earnedModel)
	if grown != 2*4_096 {
		t.Fatalf("output window = %v after one answer, want %v: a success buys a whole request", grown, 2*4_096)
	}

	limiter.ObserveWeights(map[string]map[string]float64{earnedModel: {"host": 100}})

	if _, after := limiter.windowsOf(t, "host", earnedModel); after != grown {
		t.Fatalf("output window = %v after a snapshot that moved no weight, want the %v it earned", after, grown)
	}
}

// Test flow:
//  1. Build a limiter and observe a weight of 2100 for "host", enough to buy 1.68 requests.
//  2. Drive concurrent requests for "host" via `concurrentRequests`.
//  3. Assert it admits 2 requests, rounding the fractional buy up.
func TestAWeightBuyingMostOfASecondRequestIsNotRoundedAway(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(weightedConfig(), fixedNow(earnedEpoch))
	limiter.ObserveWeights(map[string]map[string]float64{earnedModel: {"host": 2_100}})

	if admitted := concurrentRequests(t, limiter, "host"); admitted != 2 {
		t.Fatalf("a host whose weight buys 1.68 requests took %d, want the 2 it rounds to", admitted)
	}
}

// Test flow:
//  1. Build a limiter, observe a weight of 10000 for "host", and acquire 8 requests, filling the window.
//  2. Report 2 successful results, then release all 8 held requests.
//  3. Assert the output window grew to 10 requests after the two successes.
//  4. Observe the host earning more weight (11000) and assert the grown window is unchanged.
func TestEarningMoreWeightDoesNotTakeBackAGrownWindow(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(weightedConfig(), fixedNow(earnedEpoch))
	limiter.ObserveWeights(map[string]map[string]float64{earnedModel: {"host": 10_000}})

	cost := TokenCost{Input: 40_000, Output: 4_096}
	held := make([]func(), 0, 8)
	for range 8 {
		release, admission := limiter.Acquire("host", earnedModel, cost)
		if admission != AdmissionOpen {
			t.Fatalf("the host refused a request inside the 8 its weight buys: %v", admission)
		}
		held = append(held, release)
	}
	for range 2 {
		limiter.OnResult(Result{Participant: "host", Model: earnedModel, Verdict: Success, Carried: cost})
	}
	for _, release := range held {
		release()
	}

	_, grown := limiter.windowsOf(t, "host", earnedModel)
	if grown != 10*4_096 {
		t.Fatalf("output window = %v after two answers on a full window, want %v", grown, 10*4_096)
	}

	limiter.ObserveWeights(map[string]map[string]float64{earnedModel: {"host": 11_000}})

	if _, after := limiter.windowsOf(t, "host", earnedModel); after != grown {
		t.Fatalf("output window = %v after the host earned more weight, want the %v it had", after, grown)
	}
}
