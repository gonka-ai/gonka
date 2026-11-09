package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	echoTestVocab = 16
	echoTestSeed  = uint64(1234)
	echoTestSteps = 300
)

// A small model: every position has a random distribution over the whole
// vocabulary, which fits inside the validator's top logprobs.
func echoTestLogprobs() []map[int]float64 {
	rng := rand.New(rand.NewSource(7))
	out := make([]map[int]float64, echoTestSteps)
	for t := range out {
		logits := make([]float64, echoTestVocab)
		z := 0.0
		for i := range logits {
			logits[i] = rng.NormFloat64() * 2
			z += math.Exp(logits[i])
		}
		out[t] = make(map[int]float64, echoTestVocab)
		for i, l := range logits {
			out[t][i] = l - math.Log(z)
		}
	}
	return out
}

// What a tape sampler outputs: the argmax of logprob + noise at every position.
func tapeAnswer(logprobs []map[int]float64, seed uint64) []int {
	out := make([]int, len(logprobs))
	for t, lp := range logprobs {
		best := math.Inf(-1)
		for tok, l := range lp {
			if s := l + TapeNoise(seed, uint32(t), echoTestVocab, tok); s > best {
				best, out[t] = s, tok
			}
		}
	}
	return out
}

func greedyAnswer(logprobs []map[int]float64) []int {
	out := make([]int, len(logprobs))
	for t, lp := range logprobs {
		best := math.Inf(-1)
		for tok, l := range lp {
			if l > best {
				best, out[t] = l, tok
			}
		}
	}
	return out
}

var plainParams = TapeParams{Temperature: 1, TopP: 1}

func TestScoreEcho_TheTapeSamplerLooksHonest(t *testing.T) {
	lp := echoTestLogprobs()
	s := ScoreEcho(lp, tapeAnswer(lp, echoTestSeed), echoTestSeed, echoTestVocab, plainParams)
	require.Equal(t, echoTestSteps, s.Steps)
	require.Zero(t, s.Count)
	require.InDelta(t, eulerGamma, s.MeanR, 0.25)
	require.Greater(t, s.Z, -3.0)
	require.True(t, EchoThresholdsFor("Qwen/Qwen3-0.6B").Pass(s))
}

func TestScoreEcho_SamplingOffTheTapeFails(t *testing.T) {
	lp := echoTestLogprobs()
	s := ScoreEcho(lp, greedyAnswer(lp), echoTestSeed, echoTestVocab, plainParams)
	require.Less(t, s.Z, -3.0)
	require.Greater(t, s.Count, echoTestSteps/5)
	require.False(t, EchoThresholdsFor("Qwen/Qwen3-0.6B").Pass(s))
}

func TestScoreEcho_AnotherSeedIsAnotherTape(t *testing.T) {
	lp := echoTestLogprobs()
	s := ScoreEcho(lp, tapeAnswer(lp, echoTestSeed+1), echoTestSeed, echoTestVocab, plainParams)
	require.Less(t, s.Z, -3.0)
}

func TestScoreEcho_TemperatureAndTopPAreTheExecutorsOwn(t *testing.T) {
	// The executor sampled at T 0.7 with top_p 0.9: scored under those
	// parameters it is honest; scored as T 1 its tokens stop being the winners.
	lp := echoTestLogprobs()
	params := TapeParams{Temperature: 0.7, TopP: 0.9, TopK: echoTestVocab}
	out := make([]int, len(lp))
	for pos, toks := range lp {
		kept, _, _ := keptSet(toks, 0, params)
		best := math.Inf(-1)
		for _, c := range kept {
			if s := c.score + TapeNoise(echoTestSeed, uint32(pos), echoTestVocab, c.token); s > best {
				best, out[pos] = s, c.token
			}
		}
	}
	own := ScoreEcho(lp, out, echoTestSeed, echoTestVocab, params)
	require.Zero(t, own.Count)
	asPlain := ScoreEcho(lp, out, echoTestSeed, echoTestVocab, plainParams)
	require.Greater(t, asPlain.Count, echoTestSteps/20)
	require.Less(t, asPlain.Z, own.Z)
}

func TestStoredTokenIDs_JSONAndStream(t *testing.T) {
	prompt, out, err := StoredTokenIDs([]byte(`{"prompt_token_ids":[1,2,3],"choices":[{"token_ids":[4,5],"finish_reason":"stop"}]}`))
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3}, prompt)
	require.Equal(t, []int{4, 5}, out)

	streamed, _ := json.Marshal(map[string][]string{"events": {
		`data: {"id":"x","prompt_token_ids":[1,2,3],"choices":[{"delta":{"role":"assistant","content":""},"token_ids":[]}]}`,
		`data: {"id":"x","choices":[{"delta":{"content":"a"},"token_ids":[4]}]}`,
		`data: {"id":"x","choices":[{"delta":{"content":"b"},"token_ids":[5,6],"finish_reason":"stop"}]}`,
		`data: {"id":"x","choices":[],"usage":{"completion_tokens":3}}`,
		`data: [DONE]`,
	}})
	prompt, out, err = StoredTokenIDs(streamed)
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3}, prompt)
	require.Equal(t, []int{4, 5, 6}, out)

	_, _, err = StoredTokenIDs([]byte(`{"choices":[{"message":{"content":"no ids"}}]}`))
	require.Error(t, err)
}

func echoServer(t *testing.T, lp []map[int]float64, promptLen int) func(context.Context, []byte) (*http.Response, error) {
	return func(_ context.Context, body []byte) (*http.Response, error) {
		var req struct {
			Prompt         []int `json:"prompt"`
			PromptLogprobs int   `json:"prompt_logprobs"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		require.Equal(t, EchoTopLogprobs, req.PromptLogprobs)
		positions := make([]interface{}, len(req.Prompt))
		for i := promptLen; i < len(req.Prompt); i++ {
			entry := map[string]map[string]float64{}
			for tok, l := range lp[i-promptLen] {
				entry[fmt.Sprint(tok)] = map[string]float64{"logprob": l}
			}
			positions[i] = entry
		}
		resp, _ := json.Marshal(map[string]interface{}{"choices": []interface{}{map[string]interface{}{"prompt_logprobs": positions}}})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(resp))}, nil
	}
}

func TestExecuteEchoValidation_ScoresTheStoredAnswer(t *testing.T) {
	lp := echoTestLogprobs()
	prompt := []int{10, 11, 12}
	promptPayload := []byte(`{"model":"Qwen/Qwen3-0.6B","messages":[{"role":"user","content":"hi"}]}`)
	seed := uint64(int64(validationReplaySeed("42")))
	for _, tc := range []struct {
		name  string
		out   []int
		valid bool
	}{
		{"on the tape", tapeAnswer(lp, seed), true},
		{"off the tape", greedyAnswer(lp), false},
	} {
		stored, _ := json.Marshal(map[string]interface{}{"prompt_token_ids": prompt,
			"choices": []interface{}{map[string]interface{}{"token_ids": tc.out, "finish_reason": "length"}}})
		result, err := ExecuteEchoValidation(context.Background(), "42", promptPayload, stored,
			echoServer(t, lp, len(prompt)), uint64(len(prompt)), uint64(len(tc.out)))
		require.NoError(t, err, tc.name)
		require.IsType(t, &EchoValidationResult{}, result, tc.name)
		require.Equal(t, tc.valid, result.IsSuccessful(), tc.name)
	}
}

func TestExecuteEchoValidation_InflatedCountsAndRejectedIDsFail(t *testing.T) {
	lp := echoTestLogprobs()
	out := tapeAnswer(lp, uint64(int64(validationReplaySeed("42"))))
	stored, _ := json.Marshal(map[string]interface{}{"prompt_token_ids": []int{10, 11, 12},
		"choices": []interface{}{map[string]interface{}{"token_ids": out}}})
	promptPayload := []byte(`{"model":"Qwen/Qwen3-0.6B","messages":[{"role":"user","content":"hi"}]}`)

	result, err := ExecuteEchoValidation(context.Background(), "42", promptPayload, stored,
		echoServer(t, lp, 3), 3, uint64(len(out))+40)
	require.NoError(t, err)
	require.IsType(t, &InvalidInferenceResult{}, result)

	reject := func(context.Context, []byte) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(bytes.NewReader([]byte(`{"error":{"message":"bad ids"}}`)))}, nil
	}
	result, err = ExecuteEchoValidation(context.Background(), "42", promptPayload, stored, reject, 3, uint64(len(out)))
	require.NoError(t, err)
	require.IsType(t, &InvalidInferenceResult{}, result)
}

func TestEchoThresholds_FlipBoundGrowsWithLength(t *testing.T) {
	th := EchoThresholdsFor("deepseek-ai/DeepSeek-V4-Flash-0731")
	// 500 positions at an honest 5 %: 25 expected, the 99.5 % bound at 38.
	require.True(t, th.Pass(EchoSignals{Steps: 500, Count: 38, Z: 0}))
	require.False(t, th.Pass(EchoSignals{Steps: 500, Count: 39, Z: 0}))
	require.False(t, th.Pass(EchoSignals{Steps: 500, Count: 0, Z: -3.1}))
	require.False(t, th.Pass(EchoSignals{Steps: 500, Count: 0, MaxGap: 5.1}))
	// MiniMax: 500 positions at an honest 6 %: 30 expected, the 99.5 % bound at 44.
	mm := EchoThresholdsFor("MiniMaxAI/MiniMax-M2.7")
	require.True(t, mm.Pass(EchoSignals{Steps: 500, Count: 44, Z: 0}))
	require.False(t, mm.Pass(EchoSignals{Steps: 500, Count: 45, Z: 0}))
	// GLM: 500 positions at an honest 4 %: 20 expected, the 99.5 % bound at 31.
	glm := EchoThresholdsFor("zai-org/GLM-5.3-Flash")
	require.True(t, glm.Pass(EchoSignals{Steps: 500, Count: 31, Z: 0}))
	require.False(t, glm.Pass(EchoSignals{Steps: 500, Count: 32, Z: 0}))
	require.False(t, glm.Pass(EchoSignals{Steps: 500, Count: 0, MaxGap: 5.6}))
	// Without a calibration only Z decides.
	require.True(t, EchoThresholdsFor("other/model").Pass(EchoSignals{Steps: 500, Count: 200, MaxGap: 20, Z: -2.9}))
}
