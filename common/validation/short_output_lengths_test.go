package validation

import (
	"math"
	"testing"

	"common/completionapi"

	"github.com/stretchr/testify/require"
)

const deepSeekThreshold = 0.900

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

// Test flow:
// 1. Join the honest pairs end to end and build a substitute proof with every logprob scaled by 3.
// 2. At every length 1..300, in processed and raw modes, the honest output passes and the substitute fails.
// 3. Across N = 64 and N = 100 the score steps by no more than the allowance-adjusted mean or the legacy score.
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

// Test flow:
// 1. Score honest and substitute outputs of every length 1..300 in processed and raw modes.
// 2. The score never exceeds the legacy max(100, N) score, and from N = 100 equals it.
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
