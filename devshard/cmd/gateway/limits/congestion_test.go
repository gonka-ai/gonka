package limits

import (
	"testing"
)

func testFactors() CongestionFactors {
	return CongestionFactors{Soft: 0.85, Hard: 0.70, Severe: 0.50, Cross: 0.90}
}

// Test flow:
//  1. Build a table mapping each `Verdict` to its expected tier, dimension and breaker effect.
//  2. Assert every verdict from Success through DecodeStalled has a row in the table.
//  3. For each case, call `responseFor` and assert the returned tier, dimension and breaker effect match, varying the verdict under test.
func TestEveryVerdictNamesItsTierDimensionAndBreaker(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		verdict       Verdict
		wantTier      tier
		wantDimension dimension
		wantBreaker   breakerEffect
	}{
		{"success", Success, tierNone, dimensionNone, breakerRecovers},
		{"late success", LateSuccess, tierNone, dimensionNone, breakerRecovers},
		{"model outcome", ModelOutcome, tierNone, dimensionNone, breakerUntouched},
		{"overload", Overload, tierSoft, dimensionBoth, breakerUntouched},
		{"upstream fault", UpstreamFault, tierHard, dimensionBoth, breakerUntouched},
		{"empty answer", EmptyAnswer, tierHard, dimensionBoth, breakerUntouched},
		{"empty answer left open", EmptyAnswerLeftOpen, tierSevere, dimensionBoth, breakerCounts},
		{"transport fault", TransportFault, tierNone, dimensionNone, breakerCounts},
		{"decode stalled", DecodeStalled, tierSevere, dimensionOutput, breakerUntouched},
		{"missed receipt deadline", MissedReceiptDeadline, tierSevere, dimensionBoth, breakerUntouched},
		{"missed first token deadline", MissedFirstTokenDeadline, tierSevere, dimensionInput, breakerUntouched},
	}

	named := make(map[Verdict]bool, len(testCases))
	for _, testCase := range testCases {
		named[testCase.verdict] = true
	}
	for verdict := Success; verdict <= DecodeStalled; verdict++ {
		if !named[verdict] {
			t.Errorf("verdict %d has no row: a verdict nobody named falls through responseFor to the inert answer and moves nothing", verdict)
		}
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := responseFor(testCase.verdict)
			if got.tier != testCase.wantTier {
				t.Errorf("tier = %q, want %q: the ladder decides how hard this verdict narrows", got.tier, testCase.wantTier)
			}
			if got.dimension != testCase.wantDimension {
				t.Errorf("dimension = %q, want %q: narrowing the wrong window starves the side that was healthy", got.dimension, testCase.wantDimension)
			}
			if got.breaker != testCase.wantBreaker {
				t.Errorf("breaker = %v, want %v: the cutoff counts faults the host never answered", got.breaker, testCase.wantBreaker)
			}
		})
	}
}

// Test flow:
//  1. Build `testFactors` and a table of narrowing tier/blamed-dimension combinations with expected input and output multipliers.
//  2. For each case, call `narrowingFor` and assert the blamed window takes its tier's factor while the other takes the cross factor, varying which dimension is blamed and how severely.
func TestABlamedWindowTakesItsTierAndTheOtherTakesTheCrossFactor(t *testing.T) {
	t.Parallel()

	factors := testFactors()
	for _, testCase := range []struct {
		name          string
		narrowingTier tier
		blamed        dimension
		wantInput     float64
		wantOutput    float64
	}{
		{"prefill blamed", tierSevere, dimensionInput, 0.50, 0.90},
		{"decode blamed", tierSevere, dimensionOutput, 0.90, 0.50},
		{"host blamed as a whole", tierHard, dimensionBoth, 0.70, 0.70},
		{"soft congestion on prefill", tierSoft, dimensionInput, 0.85, 0.90},
		{"nothing blamed", tierNone, dimensionNone, 1, 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := factors.narrowingFor(testCase.narrowingTier, testCase.blamed)
			if got.input != testCase.wantInput || got.output != testCase.wantOutput {
				t.Fatalf("narrowing = (%v, %v), want (%v, %v): prefill and decode share one device, so the window that was not blamed still gives ground",
					got.input, got.output, testCase.wantInput, testCase.wantOutput)
			}
		})
	}
}

// Test flow:
//  1. Call `narrowingFor` with `tierSevere` and `dimensionBoth`.
//  2. Assert both input and output narrow to the same severe factor, with no separate cross-dimension penalty.
func TestAHostWideSignalCarriesNoCrossFactor(t *testing.T) {
	t.Parallel()

	got := testFactors().narrowingFor(tierSevere, dimensionBoth)
	if got.input != 0.50 || got.output != 0.50 {
		t.Fatalf("narrowing = (%v, %v), want (0.5, 0.5): a signal that blames the host as a whole is not a cross-dimension penalty", got.input, got.output)
	}
}

// Test flow:
//  1. Grow a 1000-token window by a small 10-token answer with a 100-token step.
//  2. Assert the window widens by exactly one step, not by the answer's own size.
//  3. Grow the resulting window again by a much larger answer.
//  4. Assert it again widens by exactly one step.
func TestAnAnsweredRequestWidensTheWindowByOneWholeRequest(t *testing.T) {
	t.Parallel()

	const step = 100.0

	window := grownBy(1000, 10, step)

	if window != 1000+step {
		t.Fatalf("window = %v after one answer, want %v: a success is worth a whole request of room", window, 1000+step)
	}
	if again := grownBy(window, 4_000, step); again != window+step {
		t.Fatalf("window = %v after a much larger answer, want %v: the step is the model's price, not the answer's", again, window+step)
	}
}

// Test flow:
//  1. Grow a 1000-token window by an answer that carried zero tokens.
//  2. Assert the window is unchanged.
func TestAnIdleOrCostlessAnswerDoesNotGrowTheWindow(t *testing.T) {
	t.Parallel()

	if got := grownBy(1000, 0, 100); got != 1000 {
		t.Fatalf("window = %v, want it unchanged at 1000: an answer that carried nothing proves no throughput", got)
	}
}

// Test flow:
//  1. Build a table of `Pressure` readings against a fixed slack, from unobserved to both sides slower than their best.
//  2. For each case, call `congestedDimension` and assert it names the expected dimension, varying which side (or neither) is slower.
func TestDelayPressureNamesTheCongestedWindow(t *testing.T) {
	t.Parallel()

	const slack = 0.3
	for _, testCase := range []struct {
		name     string
		pressure Pressure
		want     dimension
	}{
		{"unobserved", Pressure{}, dimensionNone},
		{"both at their best", Pressure{Input: 1, Output: 1}, dimensionNone},
		{"inside the slack", Pressure{Input: 1.29, Output: 1.29}, dimensionNone},
		{"prefill slower than its best", Pressure{Input: 1.5, Output: 1}, dimensionInput},
		{"decode slower than its best", Pressure{Input: 1, Output: 1.5}, dimensionOutput},
		{"both slower", Pressure{Input: 1.5, Output: 1.5}, dimensionBoth},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := testCase.pressure.congestedDimension(slack); got != testCase.want {
				t.Fatalf("congested dimension = %q, want %q: a host slower than its own best is congested before it has failed anything", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Call `narrowedTo` with a window of 100, a factor of 0.5 and a floor of 80.
//  2. Assert the result stops at the floor instead of narrowing below it.
func TestNarrowingStopsAtTheFloor(t *testing.T) {
	t.Parallel()

	if got := narrowedTo(100, 0.5, 80); got != 80 {
		t.Fatalf("window = %v, want the floor 80: a host a run of bad answers narrowed still takes enough work to earn its window back", got)
	}
}

// Test flow:
//  1. Call `narrowedTo` with a window of 1 token, a factor of 0.5 and a floor of 0.
//  2. Assert the result stays at 1, never narrowing to zero.
func TestNarrowingNeverGoesBelowOneToken(t *testing.T) {
	t.Parallel()

	if got := narrowedTo(1, 0.5, 0); got != 1 {
		t.Fatalf("window = %v, want 1: a window of zero tokens can never admit anything again", got)
	}
}

// Test flow:
//  1. Grow a 1000-token window by a 250-token answer with a step of 0.
//  2. Assert the window is unchanged.
func TestAWindowWithNoStepCannotGrow(t *testing.T) {
	t.Parallel()

	if got := grownBy(1000, 250, 0); got != 1000 {
		t.Fatalf("window = %v, want it unchanged: a model with no price per request has no rung to climb", got)
	}
}
