package validation

import (
	"math"
	"testing"

	"common/completionapi"

	"github.com/stretchr/testify/require"
)

// deepSeekThreshold is DeepSeek-V4-Flash-0731's live pass bar, the most lenient of the enforced models.
const deepSeekThreshold = 0.900

// substituted is a substitute model's proof: every claimed logprob scaled by 3, so each position
// scores up to 0.25, under the per-position ceiling and far above honest noise.
func substituted(validation []completionapi.Logprob) []completionapi.Logprob {
	out := make([]completionapi.Logprob, len(validation))
	for i, position := range validation {
		top := make([]completionapi.TopLogprobs, len(position.TopLogprobs))
		for j, entry := range position.TopLogprobs {
			top[j] = completionapi.TopLogprobs{Token: entry.Token, Logprob: entry.Logprob * 3}
		}
		out[i] = completionapi.Logprob{Token: position.Token, TopLogprobs: top}
	}
	return out
}

// Every output length 1..300 in both logprobs modes: the honest real pairs, joined end to end,
// pass; the substitute fails; and across N = 64 and N = 100 the score steps by no more than the
// allowance-adjusted mean or the legacy score does, so the legacy floor adds no jump.
func TestShortOutputScoringAcrossLengths(t *testing.T) {
	var executor, validator []completionapi.Logprob
	for _, pair := range loadRealPairs(t) {
		executor = append(executor, pair.Executor...)
		validator = append(validator, pair.Validator...)
	}
	substitute := substituted(validator)

	for _, mode := range []string{"processed", "raw_logprobs"} {
		score := func(original []completionapi.Logprob, n int) ValidationResult {
			return CompareLogitsWithPolicy(original[:n], validator[:n], testBase, DefaultShortOutputScoringPolicy, mode)
		}
		for n := 1; n <= 300; n++ {
			honest, fake := score(executor, n), score(substitute, n)
			require.Truef(t, passes(honest, miniMaxThreshold), "%s N=%d honest must pass, got %#v", mode, n, honest)
			require.Falsef(t, passes(fake, deepSeekThreshold), "%s N=%d substitute must fail, got %#v", mode, n, fake)
		}

		for _, n := range []int{64, 100} {
			for name, original := range map[string][]completionapi.Logprob{"honest": executor, "substitute": substitute} {
				// The score is the lower of the two, so it steps by no more than the larger of their steps.
				allowanceOnly := func(k int) float64 {
					distances, err := scorePositions(original[:k], validator[:k])
					require.NoError(t, err)
					sum := 0.0
					for _, distance := range distances {
						sum += distance
					}
					return 1 - (sum/float64(k) - DefaultShortOutputScoringPolicy.MeanAllowance/math.Sqrt(float64(k)))
				}
				legacy := func(k int) float64 {
					return CompareLogits(original[:k], validator[:k], testBase).(*SimilarityValidationResult).Value
				}
				before := score(original, n-1).(*SimilarityValidationResult).Value
				after := score(original, n).(*SimilarityValidationResult).Value
				require.Lessf(t, after, 1.0, "%s N=%d: the clamp at 1 would hide a step", name, n)
				bound := max(math.Abs(allowanceOnly(n)-allowanceOnly(n-1)), math.Abs(legacy(n)-legacy(n-1)))
				require.LessOrEqualf(t, math.Abs(after-before), bound+1e-12, "%s %s N=%d", mode, name, n)
			}
		}
	}
}

// The mean-distance check is never more lenient than the legacy max(100, N) score, and from
// N = 100 on, where legacy already divides by N, it is the legacy score.
func TestShortOutputMeanDistanceNeverMoreLenientThanLegacy(t *testing.T) {
	var executor, validator []completionapi.Logprob
	for _, pair := range loadRealPairs(t) {
		executor = append(executor, pair.Executor...)
		validator = append(validator, pair.Validator...)
	}
	for _, mode := range []string{"processed", "raw_logprobs"} {
		for name, original := range map[string][]completionapi.Logprob{"honest": executor, "substitute": substituted(validator)} {
			for n := 1; n <= 300; n++ {
				legacy := CompareLogits(original[:n], validator[:n], testBase).(*SimilarityValidationResult).Value
				short, ok := CompareLogitsWithPolicy(original[:n], validator[:n], testBase, DefaultShortOutputScoringPolicy, mode).(*SimilarityValidationResult)
				require.Truef(t, ok, "%s %s N=%d", mode, name, n)
				require.LessOrEqualf(t, short.Value, legacy+1e-12, "%s %s N=%d", mode, name, n)
				if n >= 100 {
					require.InDeltaf(t, legacy, short.Value, 1e-12, "%s %s N=%d", mode, name, n)
				}
			}
		}
	}
}
