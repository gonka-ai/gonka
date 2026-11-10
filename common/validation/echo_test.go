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
	"strconv"
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
	s := ScoreEcho(lp, tapeAnswer(lp, echoTestSeed), echoTestSeed, echoTestVocab, plainParams, nil)
	require.Equal(t, echoTestSteps, s.Steps)
	require.Zero(t, s.Count)
	require.InDelta(t, eulerGamma, s.MeanR, 0.25)
	require.Greater(t, s.Z, -3.0)
	require.True(t, EchoThresholdsFor("Qwen/Qwen3-0.6B").Pass(s))
}

func TestScoreEcho_SamplingOffTheTapeFails(t *testing.T) {
	lp := echoTestLogprobs()
	s := ScoreEcho(lp, greedyAnswer(lp), echoTestSeed, echoTestVocab, plainParams, nil)
	require.Less(t, s.Z, -3.0)
	require.Greater(t, s.Count, echoTestSteps/5)
	require.False(t, EchoThresholdsFor("Qwen/Qwen3-0.6B").Pass(s))
}

func TestScoreEcho_AnotherSeedIsAnotherTape(t *testing.T) {
	lp := echoTestLogprobs()
	s := ScoreEcho(lp, tapeAnswer(lp, echoTestSeed+1), echoTestSeed, echoTestVocab, plainParams, nil)
	require.Less(t, s.Z, -3.0)
}

func TestScoreEcho_TemperatureAndTopPAreTheExecutorsOwn(t *testing.T) {
	// The executor sampled at T 0.7 with top_p 0.9: scored under those
	// parameters it is honest; scored as T 1 its tokens stop being the winners.
	lp := echoTestLogprobs()
	params := TapeParams{Temperature: 0.7, TopP: 0.9, TopK: echoTestVocab}
	out := make([]int, len(lp))
	for pos, toks := range lp {
		kept, _, _ := keptSet(toks, 0, params, nil)
		best := math.Inf(-1)
		for _, c := range kept {
			if s := c.score + TapeNoise(echoTestSeed, uint32(pos), echoTestVocab, c.token); s > best {
				best, out[pos] = s, c.token
			}
		}
	}
	own := ScoreEcho(lp, out, echoTestSeed, echoTestVocab, params, nil)
	require.Zero(t, own.Count)
	asPlain := ScoreEcho(lp, out, echoTestSeed, echoTestVocab, plainParams, nil)
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

func echoServer(t *testing.T, lp []map[int]float64, prompt []int) func(context.Context, string, []byte) (*http.Response, error) {
	promptLen := len(prompt)
	return func(_ context.Context, path string, body []byte) (*http.Response, error) {
		if path == ChatPath {
			var req struct {
				MaxTokens      int    `json:"max_tokens"`
				ReturnTokenIDs bool   `json:"return_token_ids"`
				Stream         bool   `json:"stream"`
				Stop           any    `json:"stop"`
				LogprobsMode   string `json:"logprobs_mode"`
				TopLogprobs    int    `json:"top_logprobs"`
				Enforced       *struct {
					Tokens []struct {
						Token string `json:"token"`
					} `json:"tokens"`
				} `json:"enforced_tokens"`
			}
			require.NoError(t, json.Unmarshal(body, &req))
			require.True(t, req.ReturnTokenIDs)
			require.False(t, req.Stream)
			if req.Enforced == nil {
				require.Equal(t, 1, req.MaxTokens)
				resp, _ := json.Marshal(map[string]interface{}{"prompt_token_ids": prompt,
					"choices": []interface{}{map[string]interface{}{"token_ids": []int{0}, "finish_reason": "length"}}})
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(resp))}, nil
			}
			// A replay: the engine's processed logprobs at T 1 are the raw ones.
			require.Equal(t, len(req.Enforced.Tokens), req.MaxTokens)
			require.Equal(t, "processed_logprobs", req.LogprobsMode)
			require.Equal(t, EchoTopLogprobs, req.TopLogprobs)
			require.Nil(t, req.Stop)
			ids := make([]int, len(req.Enforced.Tokens))
			content := make([]interface{}, len(ids))
			for i, tok := range req.Enforced.Tokens {
				id, err := strconv.Atoi(tok.Token)
				require.NoError(t, err)
				ids[i] = id
				var top []map[string]interface{}
				for cand, l := range lp[i] {
					top = append(top, map[string]interface{}{"token": fmt.Sprint(cand), "logprob": l})
				}
				content[i] = map[string]interface{}{"token": fmt.Sprintf("token_id:%d", id), "logprob": lp[i][id], "top_logprobs": top}
			}
			resp, _ := json.Marshal(map[string]interface{}{"choices": []interface{}{map[string]interface{}{
				"token_ids": ids, "finish_reason": "length", "logprobs": map[string]interface{}{"content": content}}}})
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(resp))}, nil
		}
		require.Equal(t, EchoPath, path)
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
	// Explicit sampling fields: without them the model's generation_config would apply.
	promptPayload := []byte(`{"model":"Qwen/Qwen3-0.6B","messages":[{"role":"user","content":"hi"}],"max_tokens":300,"temperature":1,"top_p":1,"top_k":0}`)
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
			echoServer(t, lp, prompt), uint64(len(prompt)), uint64(len(tc.out)))
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
		echoServer(t, lp, []int{10, 11, 12}), 3, uint64(len(out))+40)
	require.NoError(t, err)
	require.IsType(t, &InvalidInferenceResult{}, result)

	reject := func(context.Context, string, []byte) (*http.Response, error) {
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

// Before min_tokens the engine masks the end tokens, so a position whose
// winner would have been an end token is honest when the next token wins.
func TestScoreEcho_AMaskedAnswerTokenCountsButIsNotScored(t *testing.T) {
	lp := echoTestLogprobs()
	out := tapeAnswer(lp, echoTestSeed)
	plain := ScoreEcho(lp, out, echoTestSeed, echoTestVocab, plainParams, nil)
	require.Zero(t, plain.Count)
	masked := make([]map[int]float64, len(lp))
	copy(masked, lp)
	masked[5] = map[int]float64{}
	for tok, l := range lp[5] {
		masked[5][tok] = l
	}
	masked[5][out[5]] = -9999
	s := ScoreEcho(masked, out, echoTestSeed, echoTestVocab, plainParams, nil)
	require.Equal(t, 1, s.Count)
	require.Equal(t, 1, s.Masked)
	require.Zero(t, s.MaxGap)
	require.InDelta(t, plain.MeanR, s.MeanR, 0.01)
	require.Greater(t, s.Z, -3.0)
}

func TestScoreEcho_EndTokensAreMaskedBeforeMinTokens(t *testing.T) {
	lp := echoTestLogprobs()
	eos := []int{15}
	masked := TapeParams{Temperature: 1, TopP: 1, MinTokens: echoTestSteps}
	out := make([]int, len(lp))
	for pos, toks := range lp {
		best := math.Inf(-1)
		for tok, l := range toks {
			if tok == eos[0] {
				continue
			}
			if s := l + TapeNoise(echoTestSeed, uint32(pos), echoTestVocab, tok); s > best {
				best, out[pos] = s, tok
			}
		}
	}
	with := ScoreEcho(lp, out, echoTestSeed, echoTestVocab, masked, eos)
	without := ScoreEcho(lp, out, echoTestSeed, echoTestVocab, plainParams, eos)
	require.Equal(t, 0, with.Count)
	require.Greater(t, without.Count, 0)
	require.Greater(t, with.Z, -3.0)
}

func TestAnswerShapeReason(t *testing.T) {
	const model = "Qwen/Qwen3-0.6B"
	p := TapeParams{MinTokens: 64, MaxTokens: 256}
	prompt := []int{1, 2, 3}
	long := make([]int, 256)
	for _, tc := range []struct {
		name   string
		out    []int
		finish string
		stop   bool
		want   string
	}{
		{"stop on an end token", append(make([]int, 100), 151645), "stop", false, ""},
		{"stop on a plain token", append(make([]int, 100), 7), "stop", false, "Answer stopped without an end token."},
		{"stop strings end anywhere", append(make([]int, 100), 7), "stop", true, ""},
		{"stop before min_tokens", []int{5, 151645}, "stop", false, "Answer stopped before min_tokens."},
		{"tool calls end on an end token", append(make([]int, 100), 151643), "tool_calls", false, ""},
		{"length at max_tokens", long, "length", false, ""},
		{"length before max_tokens", make([]int, 100), "length", false, "Answer cut before max_tokens."},
		{"unknown finish", make([]int, 100), "", false, ""},
		{"empty", nil, "stop", false, "Empty answer."},
	} {
		require.Equal(t, tc.want, answerShapeReason(model, prompt, tc.out, tc.finish, p, tc.stop), tc.name)
	}
	huge := make([]int, lengthRuleContextCap)
	require.Equal(t, "", answerShapeReason(model, huge, make([]int, 100), "length", p, false), "a context cut is not an early stop")
}

func TestStoredFinishReason_JSONAndStream(t *testing.T) {
	require.Equal(t, "stop", StoredFinishReason([]byte(`{"choices":[{"finish_reason":"stop"}]}`)))
	stream, _ := json.Marshal(map[string]interface{}{"events": []string{
		`data: {"choices":[{"finish_reason":null,"delta":{"content":"a"}}]}`,
		`data: {"choices":[{"finish_reason":"length","delta":{}}]}`,
		`data: {"choices":[],"usage":{}}`, "data: [DONE]",
	}})
	require.Equal(t, "length", StoredFinishReason(stream))
	require.Equal(t, "", StoredFinishReason([]byte(`{"choices":[{}]}`)))
}

func TestGrammarConstrained(t *testing.T) {
	for body, want := range map[string]bool{
		`{"messages":[]}`:                                             false,
		`{"response_format":{"type":"text"}}`:                         false,
		`{"response_format":{"type":"json_schema","json_schema":{}}}`: true,
		`{"response_format":{"type":"json_object"}}`:                  true,
		`{"tool_choice":"auto"}`:                                      false,
		`{"tool_choice":"required"}`:                                  true,
		`{"tool_choice":{"type":"function","function":{"name":"f"}}}`: true,
		`{"guided_regex":"[a-z]+"}`:                                   true,
	} {
		var m map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(body), &m))
		require.Equal(t, want, GrammarConstrained(m), body)
	}
}

func TestExecuteEchoValidation_GrammarAndPenaltyRequestsAreReplayed(t *testing.T) {
	lp := echoTestLogprobs()
	prompt := []int{10, 11, 12}
	seed := uint64(int64(validationReplaySeed("42")))
	for _, promptPayload := range []string{
		`{"model":"Qwen/Qwen3-0.6B","messages":[{"role":"user","content":"hi"}],"max_tokens":300,"temperature":1,"top_p":1,"top_k":0,"response_format":{"type":"json_object"}}`,
		`{"model":"Qwen/Qwen3-0.6B","messages":[{"role":"user","content":"hi"}],"max_tokens":300,"temperature":1,"top_p":1,"top_k":0,"thinking_token_budget":32}`,
		`{"model":"Qwen/Qwen3-0.6B","messages":[{"role":"user","content":"hi"}],"max_tokens":300,"temperature":1,"top_p":1,"top_k":0,"presence_penalty":0.5,"stop":["x"]}`,
		`{"model":"Qwen/Qwen2.5-7B-Instruct","messages":[{"role":"user","content":"hi"}],"max_tokens":300,"temperature":1,"top_p":1,"top_k":0}`,
	} {
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
			result, err := ExecuteEchoValidation(context.Background(), "42", []byte(promptPayload), stored,
				echoServer(t, lp, prompt), uint64(len(prompt)), uint64(len(tc.out)))
			require.NoError(t, err, tc.name)
			echo, ok := result.(*EchoValidationResult)
			require.True(t, ok, tc.name)
			require.NotEmpty(t, echo.Replay, promptPayload)
			require.Equal(t, tc.valid, result.IsSuccessful(), tc.name+" "+promptPayload)
		}
	}
}

func TestExecuteEchoValidation_ReplayWithoutTokenIDsIsUnsupported(t *testing.T) {
	lp := echoTestLogprobs()
	prompt := []int{10, 11, 12}
	out := tapeAnswer(lp, uint64(int64(validationReplaySeed("42"))))
	stored, _ := json.Marshal(map[string]interface{}{"prompt_token_ids": prompt,
		"choices": []interface{}{map[string]interface{}{"token_ids": out, "finish_reason": "length"}}})
	promptPayload := `{"model":"Qwen/Qwen3-0.6B","messages":[{"role":"user","content":"hi"}],"max_tokens":300,"temperature":1,"top_p":1,"top_k":0,"frequency_penalty":0.2}`
	inner := echoServer(t, lp, prompt)
	decoded := func(ctx context.Context, path string, body []byte) (*http.Response, error) {
		resp, err := inner(ctx, path, body)
		if err != nil || !bytes.Contains(body, []byte("enforced_tokens")) {
			return resp, err
		}
		raw, _ := io.ReadAll(resp.Body)
		raw = bytes.ReplaceAll(raw, []byte(`"token":"token_id:`), []byte(`"token":"word`))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(raw))}, nil
	}
	_, err := ExecuteEchoValidation(context.Background(), "42", []byte(promptPayload), stored, decoded,
		uint64(len(prompt)), uint64(len(out)))
	require.ErrorIs(t, err, ErrEchoUnsupported)
}

func TestReplayRequestBody_ForcesTheAnswerUnderTheEnginesSampler(t *testing.T) {
	body, err := ReplayRequestBody(map[string]interface{}{"model": "m", "messages": []interface{}{}, "stop": []string{"x"},
		"stream": true, "stream_options": map[string]interface{}{"include_usage": true}, "max_tokens": 500, "seed": 7.0}, []int{5, 6})
	require.NoError(t, err)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(body, &m))
	require.Equal(t, float64(2), m["max_tokens"])
	require.Equal(t, false, m["stream"])
	require.Equal(t, "processed_logprobs", m["logprobs_mode"])
	require.Equal(t, float64(EchoTopLogprobs), m["top_logprobs"])
	require.Equal(t, float64(7), m["seed"])
	require.NotContains(t, m, "stop")
	require.NotContains(t, m, "stream_options")
	require.JSONEq(t, `{"tokens":[{"token":"5","top_tokens":[]},{"token":"6","top_tokens":[]}]}`, string(mustJSON(m["enforced_tokens"])))
}

func TestParseReplay_TokenIDsInEitherFormat(t *testing.T) {
	body := []byte(`{"choices":[{"token_ids":[5,6],"logprobs":{"content":[
		{"token":"token_id:5","logprob":-0.5,"top_logprobs":[{"token":"5","logprob":-0.5},{"token":"token_id:7","logprob":-1.5}]},
		{"token":"6","logprob":-9999,"top_logprobs":[{"token":"1","logprob":-0.1}]}]}}]}`)
	lp, err := ParseReplay(body, []int{5, 6})
	require.NoError(t, err)
	require.Equal(t, map[int]float64{5: -0.5, 7: -1.5}, lp[0])
	require.Equal(t, map[int]float64{1: -0.1, 6: -9999}, lp[1])
	_, err = ParseReplay(body, []int{5, 9})
	require.ErrorIs(t, err, ErrEchoUnsupported)
	_, err = ParseReplay([]byte(`{"choices":[{"token_ids":[5],"logprobs":{"content":[{"token":"the","logprob":-0.5,"top_logprobs":[]}]}}]}`), []int{5})
	require.ErrorIs(t, err, ErrEchoUnsupported)
}

func mustJSON(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestExecuteEchoValidation_PromptIDsMustMatchTheValidatorsOwn(t *testing.T) {
	lp := echoTestLogprobs()
	out := tapeAnswer(lp, uint64(int64(validationReplaySeed("42"))))
	// The executor stored a shorter prompt than the request renders to.
	stored, _ := json.Marshal(map[string]interface{}{"prompt_token_ids": []int{10, 11},
		"choices": []interface{}{map[string]interface{}{"token_ids": out, "finish_reason": "length"}}})
	promptPayload := []byte(`{"model":"Qwen/Qwen3-0.6B","messages":[{"role":"user","content":"hi"}]}`)
	result, err := ExecuteEchoValidation(context.Background(), "42", promptPayload, stored,
		echoServer(t, lp, []int{10, 11, 12}), 2, uint64(len(out)))
	require.NoError(t, err)
	require.IsType(t, &InvalidInferenceResult{}, result)
	require.Equal(t, "Prompt token ids differ from the request.", result.(*InvalidInferenceResult).Reason)
}

func TestExecuteEchoValidation_AnswerShapeRulesApply(t *testing.T) {
	lp := echoTestLogprobs()
	out := tapeAnswer(lp, uint64(int64(validationReplaySeed("42"))))
	prompt := []int{10, 11, 12}
	promptPayload := []byte(`{"model":"Qwen/Qwen3-0.6B","messages":[{"role":"user","content":"hi"}],"max_tokens":300}`)
	stored, _ := json.Marshal(map[string]interface{}{"prompt_token_ids": prompt,
		"choices": []interface{}{map[string]interface{}{"token_ids": out[:200], "finish_reason": "length"}}})
	result, err := ExecuteEchoValidation(context.Background(), "42", promptPayload, stored, echoServer(t, lp, prompt), 3, 200)
	require.NoError(t, err)
	require.IsType(t, &InvalidInferenceResult{}, result)
	require.Equal(t, "Answer cut before max_tokens.", result.(*InvalidInferenceResult).Reason)
}

// A request without sampling fields runs under the model's generation_config,
// which vLLM applies by default; the validator's kept set must follow it.
func TestTapeParams_ModelDefaultsFillMissingFields(t *testing.T) {
	params := func(body string) TapeParams {
		var m map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(body), &m))
		return tapeParams(m)
	}
	require.Equal(t, TapeParams{Temperature: 1, TopP: 0.95, TopK: 40, RepetitionPenalty: 1},
		params(`{"model":"MiniMaxAI/MiniMax-M2.7","messages":[]}`))
	require.Equal(t, TapeParams{Temperature: 1, TopP: 1, TopK: 0, MinTokens: 64, MaxTokens: 256, RepetitionPenalty: 1},
		params(`{"model":"MiniMaxAI/MiniMax-M2.7","top_p":1,"top_k":0,"min_tokens":64,"max_tokens":256}`))
	require.Equal(t, TapeParams{Temperature: 1, TopP: 1, RepetitionPenalty: 1}, params(`{"model":"other/model"}`))
}

func TestTapeParams_PenalisedByRequestOrModelDefault(t *testing.T) {
	params := func(body string) TapeParams {
		var m map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(body), &m))
		return tapeParams(m)
	}
	require.False(t, params(`{"model":"MiniMaxAI/MiniMax-M2.7"}`).Penalised())
	require.True(t, params(`{"model":"MiniMaxAI/MiniMax-M2.7","frequency_penalty":0.2}`).Penalised())
	require.True(t, params(`{"model":"Qwen/Qwen2.5-7B-Instruct"}`).Penalised())
	require.False(t, params(`{"model":"Qwen/Qwen2.5-7B-Instruct","repetition_penalty":1}`).Penalised())
}
