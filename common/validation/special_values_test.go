package validation

import (
	"math"
	"testing"

	"common/completionapi"

	"github.com/stretchr/testify/require"
)

// collapsed builds one position whose processed top_logprobs have collapsed to a single token at
// ~0 with the rest at the -9999 sentinel, as top_k=1 (optionally plus logit_bias) makes vLLM
// serialize them. chosenLogprob is the validator's own logprob for the emitted token: ~0 when it is
// the forced top token, -9999 when the genuine sampler gives it no support.
func collapsed(emitted, forced string, chosenLogprob float64) completionapi.Logprob {
	top := []completionapi.TopLogprobs{{Token: forced, Logprob: -0.00001}}
	for j := 1; j < completionapi.ForcedTopLogprobs; j++ {
		top = append(top, completionapi.TopLogprobs{Token: forcedFiller(j), Logprob: outOfSupportLogprob})
	}
	return completionapi.Logprob{Token: emitted, Logprob: chosenLogprob, TopLogprobs: top}
}

func forcedFiller(j int) string { return "filler_" + string(rune('0'+j)) }

func collapsedRun(emitted, forced string, chosenLogprob float64, n int) []completionapi.Logprob {
	out := make([]completionapi.Logprob, n)
	for i := range out {
		out[i] = collapsed(emitted, forced, chosenLogprob)
	}
	return out
}

// A top_k=1 collapse that fabricates content the genuine sampler cannot emit shows the -9999
// sentinel as the validator's chosen-token logprob at every position, so the sampler-support check
// rejects it on an enforced model. The per-position distance alone would not: both collapsed top
// sets agree, so it reads ~0 (H1 #4100189).
func TestSpecialValueCollapseRejectedOnEnforcedModel(t *testing.T) {
	enforced := DefaultShortOutputScoringPolicy.ForModel("deepseek-ai/DeepSeek-V4-Flash-0731")
	require.False(t, enforced.OutputChecksLogOnly)

	run := collapsedRun("fab", "forced", outOfSupportLogprob, 8)
	claimed := collapsedRun("fab", "forced", -0.00001, 8)

	distances, err := scorePositions(claimed, run)
	require.NoError(t, err)
	require.Less(t, distances[0], enforced.OutputPositionCeiling, "collapsed top sets agree, so the distance does not catch it")

	result := CompareLogitsWithPolicy(claimed, run, testBase, enforced, "processed")
	require.IsType(t, &InvalidInferenceResult{}, result)
	require.Equal(t, "Output tokens outside the sampler support.", result.(*InvalidInferenceResult).Reason)
}

// On a model with no calibration data the same collapse is scored log-only: an output byte-identical
// to an honest run of the forced request is indistinguishable, so the mean-distance check passes it.
// Enforcing the sampler-support check here would reject the honest tokens a genuine sampler emits at
// -9999, which the calibrated enforced budget is sized to tolerate.
func TestSpecialValueCollapseLogOnlyIsResidual(t *testing.T) {
	logOnly := DefaultShortOutputScoringPolicy.ForModel("MiniMaxAI/MiniMax-M2.7")
	require.True(t, logOnly.OutputChecksLogOnly)

	run := collapsedRun("fab", "forced", outOfSupportLogprob, 8)
	claimed := collapsedRun("fab", "forced", -0.00001, 8)

	result := CompareLogitsWithPolicy(claimed, run, testBase, logOnly, "processed")
	require.IsType(t, &SimilarityValidationResult{}, result)
	require.Greater(t, result.(*SimilarityValidationResult).Value, 0.99)
}

// A non-finite executor top logprob is untrusted: positionDistance scores it at the 0.5 ceiling
// term, so the per-position ceiling check rejects it on an enforced model and with raw logprobs, and
// the mean-distance check fails it everywhere. No +Inf/-Inf/NaN claim scores as agreement.
func TestSpecialValueNonFiniteExecutorFailsClosed(t *testing.T) {
	enforced := DefaultShortOutputScoringPolicy.ForModel("deepseek-ai/DeepSeek-V4-Flash-0731")
	logOnly := DefaultShortOutputScoringPolicy.ForModel("MiniMaxAI/MiniMax-M2.7")

	for _, bad := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
		validation := collapsedRun("tok", "tok", -0.1, 8)
		claimed := make([]completionapi.Logprob, len(validation))
		for i, position := range validation {
			top := make([]completionapi.TopLogprobs, len(position.TopLogprobs))
			for j, entry := range position.TopLogprobs {
				top[j] = completionapi.TopLogprobs{Token: entry.Token, Logprob: bad}
			}
			claimed[i] = completionapi.Logprob{Token: position.Token, Logprob: bad, TopLogprobs: top}
		}

		distances, err := scorePositions(claimed, validation)
		require.NoError(t, err)
		require.Equal(t, maxPositionTerm, distances[0], "non-finite claim scores the 0.5 ceiling term")

		require.IsType(t, &InvalidInferenceResult{}, CompareLogitsWithPolicy(claimed, validation, testBase, enforced, "processed"))
		require.IsType(t, &InvalidInferenceResult{}, CompareLogitsWithPolicy(claimed, validation, testBase, enforced, "raw_logprobs"))

		result := CompareLogitsWithPolicy(claimed, validation, testBase, logOnly, "raw_logprobs")
		require.IsType(t, &SimilarityValidationResult{}, result)
		require.False(t, passes(result, miniMaxThreshold), "mean distance fails the non-finite output even log-only")
	}
}
