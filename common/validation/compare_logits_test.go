package validation

import (
	"fmt"
	"math"
	"testing"

	"common/completionapi"

	"github.com/stretchr/testify/require"
)

// lp / tl build a position: the generated token plus its top_logprobs.
func lp(token string, top ...completionapi.TopLogprobs) completionapi.Logprob {
	return completionapi.Logprob{Token: token, TopLogprobs: top}
}

func tl(token string, logprob float64) completionapi.TopLogprobs {
	return completionapi.TopLogprobs{Token: token, Logprob: logprob}
}

// The executor is untrusted: padding its top_logprobs width must not shrink the distance (H1 #3853145).
func TestCustomDistanceIgnoresExecutorTopLogprobsPadding(t *testing.T) {
	validator := []completionapi.Logprob{lp("x", tl("a", -0.5), tl("b", -1.5))}
	narrowExecutor := []completionapi.Logprob{lp("x", tl("a", -2.0), tl("b", -2.0))}
	// Same logprobs for the validator's tokens plus entries it never scores: only the width differs.
	paddedExecutor := []completionapi.Logprob{lp("x", tl("a", -2.0), tl("b", -2.0), tl("c", -50.0), tl("d", -50.0), tl("e", -50.0))}

	narrow, err := customDistance(narrowExecutor, validator)
	require.NoError(t, err)
	padded, err := customDistance(paddedExecutor, validator)
	require.NoError(t, err)

	require.Greater(t, narrow, 0.0, "sanity: distance must be non-zero")
	require.Equal(t, narrow, padded, "executor padding must not change the distance")
}

// The score depends on the position count alone, not on the per-position top_logprobs width.
func TestCustomDistanceIsWidthIndependent(t *testing.T) {
	const positions = 100
	original := make([]completionapi.Logprob, positions)
	validation := make([]completionapi.Logprob, positions)
	for i := range positions {
		width := 5
		if i%2 == 1 {
			width = 7
		}
		var executorTop, validatorTop []completionapi.TopLogprobs
		for j := range width {
			token := fmt.Sprintf("p%d_t%d", i, j)
			validatorTop = append(validatorTop, tl(token, -1.0))
			executorTop = append(executorTop, tl(token, -3.0))
		}
		original[i] = completionapi.Logprob{TopLogprobs: executorTop}
		validation[i] = completionapi.Logprob{TopLogprobs: validatorTop}
	}

	dist, err := customDistance(original, validation)
	require.NoError(t, err)
	// Every term is |−1−(−3)| / (1e-6 + 1 + 3) / 2 ≈ 0.25, unchanged by the 5/7 width mix.
	require.InDelta(t, 0.25, dist, 1e-3)
}

// A validator token the executor never listed falls back to an estimate from the executor's
// logprob spread; padding must not shift that estimate.
func TestCustomDistancePaddingDoesNotPerturbFallbackEstimate(t *testing.T) {
	validator := []completionapi.Logprob{lp("x", tl("a", -1), tl("z", -5))} // "z" is not in the executor
	narrow := []completionapi.Logprob{lp("x", tl("a", -1), tl("b", -2))}
	padded := []completionapi.Logprob{lp("x", tl("a", -1), tl("b", -2), tl("c", -50), tl("d", -50))}

	distanceNarrow, err := customDistance(narrow, validator)
	require.NoError(t, err)
	distancePadded, err := customDistance(padded, validator)
	require.NoError(t, err)

	require.Greater(t, distanceNarrow, 0.0, "sanity: the fallback token must contribute")
	require.Equal(t, distanceNarrow, distancePadded, "executor padding must not shift the fallback estimate")
}

// Matching tokens but wildly disagreeing top_logprobs: padding changes neither the score nor the verdict.
func TestCompareLogitsPaddingCannotRescueGarbage(t *testing.T) {
	const positions = 100
	build := func(executorPadding int) (original, validation []completionapi.Logprob) {
		for range positions {
			validation = append(validation, lp("t", tl("a", -1), tl("b", -2)))
			executorTop := []completionapi.TopLogprobs{tl("a", -30), tl("b", -31)}
			for j := range executorPadding {
				executorTop = append(executorTop, tl(fmt.Sprintf("pad%d", j), -50))
			}
			original = append(original, lp("t", executorTop...))
		}
		return original, validation
	}
	base := BaseValidationResult{InferenceId: "x"}
	originalNarrow, validation := build(0)
	originalPadded, _ := build(10)

	similarityNarrow := CompareLogits(originalNarrow, validation, base).(*SimilarityValidationResult).Value
	similarityPadded := CompareLogits(originalPadded, validation, base).(*SimilarityValidationResult).Value

	require.Equal(t, similarityNarrow, similarityPadded, "padding must not change the garbage score")
	require.Less(t, similarityNarrow, 0.9, "garbage must not pass validation")
}

// Validation shorter than the original is rejected before any distance math.
func TestCompareLogitsRejectsShorterValidation(t *testing.T) {
	original := []completionapi.Logprob{lp("a", tl("a", -1)), lp("b", tl("b", -1))}
	validation := []completionapi.Logprob{lp("a", tl("a", -1))}

	result := CompareLogits(original, validation, BaseValidationResult{InferenceId: "x"})

	require.IsType(t, &DifferentLengthValidationResult{}, result)
}

// A position whose generated token differs is rejected.
func TestCompareLogitsRejectsDifferentTokens(t *testing.T) {
	original := []completionapi.Logprob{lp("a", tl("a", -1))}
	validation := []completionapi.Logprob{lp("b", tl("b", -1))}

	result := CompareLogits(original, validation, BaseValidationResult{InferenceId: "x"})

	require.IsType(t, &DifferentTokensValidationResult{}, result)
}

// No executor logprobs to compare means zero distance, i.e. maximum similarity.
func TestCustomDistanceEmptyOriginalIsZeroDistance(t *testing.T) {
	distance, err := customDistance(nil, []completionapi.Logprob{lp("x", tl("a", -1))})

	require.NoError(t, err)
	require.Equal(t, 0.0, distance)
}

// A position carrying no top_logprobs cannot be scored, so the error drives similarity to 0.
func TestCustomDistanceEmptyPositionTopLogprobsErrors(t *testing.T) {
	original := []completionapi.Logprob{lp("x", tl("a", -1))}
	validation := []completionapi.Logprob{lp("x")}

	_, err := customDistance(original, validation)

	require.Error(t, err)
}

// The distance error must surface as similarity 0 and a failed result, not as a silent pass.
func TestCompareLogitsScoresZeroWhenDistanceErrors(t *testing.T) {
	original := []completionapi.Logprob{lp("x", tl("a", -1))}
	validation := []completionapi.Logprob{lp("x")} // no top_logprobs at this position

	result := CompareLogits(original, validation, BaseValidationResult{InferenceId: "x"})

	require.Equal(t, 0.0, result.(*SimilarityValidationResult).Value)
	require.False(t, result.IsSuccessful())
}

// positionDistance keys the executor's top_logprobs by token, so duplicates collapse and
// narrow the fallback estimate. That can only raise the distance — never game it downwards.
func TestCustomDistanceDuplicateExecutorTokensCannotLowerDistance(t *testing.T) {
	validator := []completionapi.Logprob{lp("x", tl("a", -1), tl("z", -5))} // "z" forces the fallback
	distinct := []completionapi.Logprob{lp("x", tl("a", -1), tl("b", -2))}
	duplicated := []completionapi.Logprob{lp("x", tl("a", -1), tl("a", -1))}

	distanceDistinct, err := customDistance(distinct, validator)
	require.NoError(t, err)
	distanceDuplicated, err := customDistance(duplicated, validator)
	require.NoError(t, err)

	require.Greater(t, distanceDuplicated, distanceDistinct, "collapsed duplicates must not score better than distinct tokens")
}

// An executor narrower than the validator is not truncated: the missing tokens fall back to
// the estimate, which stays scoreable rather than erroring out.
func TestCustomDistanceExecutorNarrowerThanValidator(t *testing.T) {
	validator := []completionapi.Logprob{lp("x", tl("a", -1), tl("b", -2), tl("c", -3))}
	narrowExecutor := []completionapi.Logprob{lp("x", tl("a", -1))}

	distance, err := customDistance(narrowExecutor, validator)

	require.NoError(t, err)
	require.Greater(t, distance, 0.0, "tokens the executor never listed must still contribute")
}

// ModifyRequestBody pins top_logprobs to ForcedTopLogprobs, but that binds the request only —
// the stored response is authored by the untrusted executor and nothing checks its width. A
// response wider than the pinned request must therefore score exactly like a conforming one.
func TestCustomDistanceIgnoresResponseWiderThanPinnedRequest(t *testing.T) {
	buildTop := func(width int, logprob float64) []completionapi.TopLogprobs {
		top := make([]completionapi.TopLogprobs, 0, width)
		for i := range width {
			top = append(top, tl(fmt.Sprintf("t%d", i), logprob))
		}
		return top
	}
	validator := []completionapi.Logprob{lp("x", buildTop(completionapi.ForcedTopLogprobs, -1)...)}
	conforming := []completionapi.Logprob{lp("x", buildTop(completionapi.ForcedTopLogprobs, -3)...)}
	overwide := []completionapi.Logprob{lp("x", buildTop(4*completionapi.ForcedTopLogprobs, -3)...)}

	distanceConforming, err := customDistance(conforming, validator)
	require.NoError(t, err)
	distanceOverwide, err := customDistance(overwide, validator)
	require.NoError(t, err)

	require.Equal(t, distanceConforming, distanceOverwide, "a response wider than the pinned request must not score differently")
}

// Padding invariance and the distance bound must hold for any logprob values, including the
// NaN/Inf shapes an untrusted executor can put in a stored response.
func FuzzCustomDistanceExecutorPadding(f *testing.F) {
	f.Add(-1.0, -5.0, -1.0, -2.0, 3)
	f.Add(0.0, 0.0, 0.0, 0.0, 0)
	f.Add(math.NaN(), 0.0, math.Inf(-1), math.Inf(1), 5)
	f.Add(-1e308, 1e308, -1e308, 1e308, 7)

	f.Fuzz(func(t *testing.T, validatorFirst, validatorSecond, executorFirst, executorSecond float64, padding int) {
		padding = padding % 16
		if padding < 0 {
			padding = -padding
		}

		// "z" is absent from the executor, so the fallback estimate is exercised too.
		validator := []completionapi.Logprob{lp("x", tl("a", validatorFirst), tl("z", validatorSecond))}
		narrow := []completionapi.Logprob{lp("x", tl("a", executorFirst), tl("b", executorSecond))}
		paddedTop := []completionapi.TopLogprobs{tl("a", executorFirst), tl("b", executorSecond)}
		for i := range padding {
			paddedTop = append(paddedTop, tl(fmt.Sprintf("pad%d", i), -50))
		}
		padded := []completionapi.Logprob{lp("x", paddedTop...)}

		distanceNarrow, err := customDistance(narrow, validator)
		require.NoError(t, err)
		distancePadded, err := customDistance(padded, validator)
		require.NoError(t, err)

		require.Equal(t, distanceNarrow, distancePadded, "executor padding must not change the distance")
		// Each term is |v−o| / (1e-6+|v|+|o|) / 2 < 0.5, so the mean can never leave [0, 0.5).
		require.False(t, math.IsNaN(distanceNarrow) || math.IsInf(distanceNarrow, 0), "distance must stay finite")
		require.GreaterOrEqual(t, distanceNarrow, 0.0)
		require.Less(t, distanceNarrow, 0.5)
	})
}

// customDistance divides by max(100, positions), so identical per-position garbage scores far
// higher on a short response than on a long one. This is what the min_tokens>=64 floor guards:
// without it a handful of positions can clear the bar. Pinned so the coupling stays visible.
func TestCompareLogitsUnderNormalizesShortOutputs(t *testing.T) {
	const chainSimilarityThreshold = 0.94 // configured bar; LegacySimilarityThreshold (0.99) is stricter
	garbageSimilarity := func(positions int) float64 {
		var original, validation []completionapi.Logprob
		for range positions {
			validation = append(validation, lp("t", tl("a", -1), tl("b", -2)))
			original = append(original, lp("t", tl("a", -30), tl("b", -31)))
		}
		result := CompareLogits(original, validation, BaseValidationResult{InferenceId: "x"})
		return result.(*SimilarityValidationResult).Value
	}

	short := garbageSimilarity(10)
	long := garbageSimilarity(100)

	require.Greater(t, short, long, "the fixed divisor dilutes short responses")
	require.True(t, SimilarityPassesThreshold(short, chainSimilarityThreshold), "10 positions of garbage still clear 0.94")
	require.False(t, SimilarityPassesThreshold(long, chainSimilarityThreshold), "100 positions of the same garbage do not")
}
