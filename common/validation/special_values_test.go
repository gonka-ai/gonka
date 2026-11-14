package validation

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"testing"

	"common/completionapi"

	"github.com/stretchr/testify/require"
)

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

// Test flow:
// 1. Build a top_k=1 collapse that fabricates content: every emitted token is out of support.
// 2. Its per-position distance stays under the ceiling.
// 3. On an enforced model it is rejected as outside the sampler support.
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

// Test flow:
// 1. Build the same fabricated top_k=1 collapse on a log-only model.
// 2. It passes with similarity above 0.99.
func TestSpecialValueCollapseLogOnlyIsResidual(t *testing.T) {
	logOnly := DefaultShortOutputScoringPolicy.ForModel("MiniMaxAI/MiniMax-M2.7")
	require.True(t, logOnly.OutputChecksLogOnly)

	run := collapsedRun("fab", "forced", outOfSupportLogprob, 8)
	claimed := collapsedRun("fab", "forced", -0.00001, 8)

	result := CompareLogitsWithPolicy(claimed, run, testBase, logOnly, "processed")
	require.IsType(t, &SimilarityValidationResult{}, result)
	require.Greater(t, result.(*SimilarityValidationResult).Value, 0.99)
}

// Test flow:
// 1. Claim +Inf, -Inf or NaN for every executor top logprob.
// 2. Each position scores the 0.5 ceiling term.
// 3. An enforced model rejects it in processed and raw modes; a log-only model fails it on mean distance.
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

func argmaxFlipRun(n int, flips ...int) (claimed, validation []completionapi.Logprob) {
	claimed = make([]completionapi.Logprob, n)
	validation = make([]completionapi.Logprob, n)
	for i := range claimed {
		token := fmt.Sprintf("t%d", i)
		claimed[i] = collapsed(token, token, -0.00001)
		validation[i] = collapsed(token, token, -0.00001)
	}
	for _, i := range flips {
		claimed[i] = collapsed("x", "x", -0.00001)
		validation[i] = collapsed("x", "y", outOfSupportLogprob)
	}
	return claimed, validation
}

// Test flow:
// 1. Build an honest top_k=1 output of 30 positions where executor and validator argmax differ at two positions.
// 2. The default sampler-support budget rejects it; the top_k=1 request's narrow-sampler budget passes it.
// 3. Under top_k=1, 15 flips of 30 pass and 16 are rejected.
func TestSamplerSupportNarrowSamplerToleratesArgmaxFlips(t *testing.T) {
	enforced := DefaultShortOutputScoringPolicy.ForModel("zai-org/GLM-5.3-Flash")
	topK1 := enforced.ForRequest(map[string]interface{}{"top_k": 1.0, "temperature": 0.7})
	require.True(t, topK1.NarrowSampler)
	const glmThreshold = 0.951

	claimed, validation := argmaxFlipRun(30, 7, 19)
	result := CompareLogitsWithPolicy(claimed, validation, testBase, enforced, "processed_logprobs")
	require.IsType(t, &InvalidInferenceResult{}, result, "the 3 % budget rejects two argmax flips in 30")
	require.Equal(t, "Output tokens outside the sampler support.", result.(*InvalidInferenceResult).Reason)
	require.True(t, passes(CompareLogitsWithPolicy(claimed, validation, testBase, topK1, "processed_logprobs"), glmThreshold),
		"two argmax flips in 30 under top_k=1")

	half := []int{0, 2, 4, 6, 8, 10, 12, 14, 16, 18, 20, 22, 24, 26, 28}
	claimed, validation = argmaxFlipRun(30, half...)
	require.True(t, passes(CompareLogitsWithPolicy(claimed, validation, testBase, topK1, "processed_logprobs"), glmThreshold),
		"15 of 30 is within ceil(50%)")

	claimed, validation = argmaxFlipRun(30, append(half, 29)...)
	result = CompareLogitsWithPolicy(claimed, validation, testBase, topK1, "processed_logprobs")
	require.IsType(t, &InvalidInferenceResult{}, result)
	require.Equal(t, "Output tokens outside the sampler support.", result.(*InvalidInferenceResult).Reason)
}

// Test flow:
// 1. Build a fabricated top_k=1 collapse under a top_k=1 request on an enforced model.
// 2. It is rejected as outside the sampler support despite the narrow-sampler budget.
func TestSpecialValueCollapseRejectedUnderNarrowSampler(t *testing.T) {
	topK1 := DefaultShortOutputScoringPolicy.ForModel("deepseek-ai/DeepSeek-V4-Flash-0731").
		ForRequest(map[string]interface{}{"top_k": 1.0, "logit_bias": map[string]interface{}{"42": 100.0}})

	run := collapsedRun("fab", "forced", outOfSupportLogprob, 8)
	claimed := collapsedRun("fab", "forced", -0.00001, 8)
	result := CompareLogitsWithPolicy(claimed, run, testBase, topK1, "processed")
	require.IsType(t, &InvalidInferenceResult{}, result)
	require.Equal(t, "Output tokens outside the sampler support.", result.(*InvalidInferenceResult).Reason)
}

// Test flow:
// 1. Derive the policy for requests with various top_k, top_p, min_p and temperature values.
// 2. NarrowSampler matches the expected value for each sampler setting.
func TestScoringPolicyForRequestNarrowSampler(t *testing.T) {
	for _, tc := range []struct {
		request map[string]interface{}
		want    bool
	}{
		{map[string]interface{}{}, false},
		{map[string]interface{}{"top_k": 1.0}, true},
		{map[string]interface{}{"top_k": 20.0}, true},
		{map[string]interface{}{"top_k": -1.0}, false},
		{map[string]interface{}{"top_k": 0.0}, false},
		{map[string]interface{}{"top_k": 40.0, "top_p": 0.95}, false},
		{map[string]interface{}{"top_k": 50.0, "top_p": 1.0}, false},
		{map[string]interface{}{"top_p": 0.8}, true},
		{map[string]interface{}{"min_p": 0.05}, true},
		{map[string]interface{}{"min_p": 0.0}, false},
		{map[string]interface{}{"top_k": "1"}, false},
		{map[string]interface{}{"top_p": 0.95, "temperature": 0.7}, false},
		{map[string]interface{}{"top_p": 0.95, "temperature": 0.6}, true},
		{map[string]interface{}{"top_p": 0.99, "temperature": 0.0}, true},
		{map[string]interface{}{"top_p": 1.0, "temperature": 0.1}, false},
		{map[string]interface{}{"temperature": 0.1}, false},
		{map[string]interface{}{"top_k": 40.0, "temperature": 0.1}, false},
		{map[string]interface{}{"top_p": 0.95, "temperature": "0.1"}, false},
	} {
		require.Equalf(t, tc.want, DefaultShortOutputScoringPolicy.ForRequest(tc.request).NarrowSampler, "%v", tc.request)
	}
}

func processedTop(top []completionapi.TopLogprobs, temperature, topP float64) []completionapi.TopLogprobs {
	out := make([]completionapi.TopLogprobs, len(top))
	norm := math.Inf(-1)
	for i, entry := range top {
		out[i] = completionapi.TopLogprobs{Token: entry.Token, Logprob: entry.Logprob / temperature}
		norm = math.Max(norm, out[i].Logprob) + math.Log1p(math.Exp(-math.Abs(norm-out[i].Logprob)))
	}
	slices.SortFunc(out, func(a, b completionapi.TopLogprobs) int { return cmp.Compare(b.Logprob, a.Logprob) })
	mass := 0.0
	for i := range out {
		out[i].Logprob -= norm
		if mass >= topP {
			out[i].Logprob = outOfSupportLogprob
		}
		mass += math.Exp(out[i].Logprob)
	}
	return out
}

func argmaxUnderSampler(pair realPair, n int, temperature, topP float64) (claimed, validation []completionapi.Logprob) {
	for i := range n {
		executorTop := processedTop(pair.Executor[i].TopLogprobs, temperature, topP)
		validatorTop := processedTop(pair.Validator[i].TopLogprobs, temperature, topP)
		token := executorTop[0].Token
		chosen := outOfSupportLogprob
		for _, entry := range validatorTop {
			if entry.Token == token {
				chosen = entry.Logprob
			}
		}
		claimed = append(claimed, completionapi.Logprob{Token: token, Logprob: executorTop[0].Logprob, TopLogprobs: executorTop})
		validation = append(validation, completionapi.Logprob{Token: token, Logprob: chosen, TopLogprobs: validatorTop})
	}
	return claimed, validation
}

// Test flow:
// 1. Rebuild processed logprobs of the honest MiniMax-M2.7 pairs under top_p=0.95 at temperatures 0.05, 0.1 and 0.7, with the executor emitting its own argmax.
// 2. At 0.05 and 0.1 the request is a narrow sampler, the 3 % budget rejects some full or prefix outputs, and the narrow-sampler budget passes all of them.
// 3. At 0.7 the request is not a narrow sampler and every output passes the 3 % budget.
func TestSamplerSupportLowTemperatureTopPIsNarrow(t *testing.T) {
	pairs := loadRealPairs(t)
	for _, model := range []string{"zai-org/GLM-5.3-Flash", "deepseek-ai/DeepSeek-V4-Flash-0731"} {
		enforced := DefaultShortOutputScoringPolicy.ForModel(model)
		for _, tc := range []struct {
			temperature float64
			narrow      bool
		}{{0.05, true}, {0.1, true}, {0.7, false}} {
			policy := enforced.ForRequest(map[string]interface{}{"top_p": 0.95, "temperature": tc.temperature})
			require.Equal(t, tc.narrow, policy.NarrowSampler, tc.temperature)
			rejectedAtDefault := 0
			for _, pair := range pairs {
				for _, n := range []int{30, 80, len(pair.Executor)} {
					claimed, validation := argmaxUnderSampler(pair, n, tc.temperature, 0.95)
					if _, invalid := CompareLogitsWithPolicy(claimed, validation, testBase, enforced, "processed").(*InvalidInferenceResult); invalid {
						rejectedAtDefault++
					}
					result := CompareLogitsWithPolicy(claimed, validation, testBase, policy, "processed")
					require.IsTypef(t, &SimilarityValidationResult{}, result, "%s %s T=%v n=%d", model, pair.Name, tc.temperature, n)
				}
			}
			require.Equal(t, tc.narrow, rejectedAtDefault > 0, "%s T=%v", model, tc.temperature)
		}
	}
}

func collapsedResponse(t *testing.T, content []completionapi.Logprob) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"id":      "test",
		"object":  "chat.completion",
		"choices": []map[string]interface{}{{"index": 0, "logprobs": map[string]interface{}{"content": content}}},
	})
	require.NoError(t, err)
	return body
}

// Test flow:
// 1. Store and replay an output with two argmax flips in 30 positions, using numeric token ids.
// 2. Validation passes for a top_k=1 request and fails for a request without a narrowed sampler.
func TestExecuteValidation_ArgmaxFlipsUnderTopK1Request(t *testing.T) {
	enforced := DefaultShortOutputScoringPolicy.ForModel("zai-org/GLM-5.3-Flash")
	claimed, validation := argmaxFlipRun(30, 7, 19)
	ids := map[string]string{"x": "7", "y": "8"}
	numeric := func(token string) string {
		if id, ok := ids[token]; ok {
			return id
		}
		ids[token] = fmt.Sprint(100 + len(ids))
		return ids[token]
	}
	for _, positions := range [][]completionapi.Logprob{claimed, validation} {
		for i := range positions {
			positions[i].Token = numeric(positions[i].Token)
			top := append([]completionapi.TopLogprobs(nil), positions[i].TopLogprobs...)
			for j := range top {
				top[j].Token = numeric(top[j].Token)
			}
			positions[i].TopLogprobs = top
		}
	}
	stored, replayed := collapsedResponse(t, claimed), collapsedResponse(t, validation)
	execute := func(ctx context.Context, body []byte) (*http.Response, error) {
		return fakeHTTPResponse(http.StatusOK, replayed), nil
	}

	for _, tc := range []struct {
		prompt string
		valid  bool
	}{
		{`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"temperature":0.7,"top_k":1}`, true},
		{`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"temperature":0.7}`, false},
	} {
		res, err := ExecuteValidationWithPolicy(context.Background(), "inf-topk1", []byte(tc.prompt), stored, execute,
			0, 0, "processed_logprobs", 0, enforced)
		require.NoError(t, err)
		require.Equalf(t, tc.valid, passes(res, 0.951), "%s", tc.prompt)
	}
}
