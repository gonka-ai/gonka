package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// responsePayloadJSON builds a minimal completion response JSON suitable for use as
// a responsePayload argument to ExecuteValidation.
// token should be a numeric string (e.g. "42") for the normal path, or "<EMPTY>" for
// the empty-sentinel path.
func responsePayloadJSON(token string, logprob float64) []byte {
	type topLP struct {
		Token   string  `json:"token"`
		Logprob float64 `json:"logprob"`
		Bytes   []int   `json:"bytes"`
	}
	type lp struct {
		Token       string  `json:"token"`
		Logprob     float64 `json:"logprob"`
		Bytes       []int   `json:"bytes"`
		TopLogprobs []topLP `json:"top_logprobs"`
	}
	type logprobs struct {
		Content []lp `json:"content"`
	}
	type choice struct {
		Index    int      `json:"index"`
		Logprobs logprobs `json:"logprobs"`
	}
	type resp struct {
		ID      string   `json:"id"`
		Object  string   `json:"object"`
		Choices []choice `json:"choices"`
	}
	r := resp{
		ID:     "test",
		Object: "chat.completion",
		Choices: []choice{{
			Logprobs: logprobs{Content: []lp{{
				Token:   token,
				Logprob: logprob,
				TopLogprobs: []topLP{
					{Token: token, Logprob: logprob},
					{Token: "99", Logprob: logprob - 1.0},
				},
			}}},
		}},
	}
	b, _ := json.Marshal(r)
	return b
}

func responsePayloadJSONWithUsage(token string, logprob float64, promptTokens, completionTokens uint64) []byte {
	type topLP struct {
		Token   string  `json:"token"`
		Logprob float64 `json:"logprob"`
		Bytes   []int   `json:"bytes"`
	}
	type lp struct {
		Token       string  `json:"token"`
		Logprob     float64 `json:"logprob"`
		Bytes       []int   `json:"bytes"`
		TopLogprobs []topLP `json:"top_logprobs"`
	}
	type logprobs struct {
		Content []lp `json:"content"`
	}
	type usage struct {
		PromptTokens     uint64 `json:"prompt_tokens"`
		CompletionTokens uint64 `json:"completion_tokens"`
	}
	type choice struct {
		Index    int      `json:"index"`
		Logprobs logprobs `json:"logprobs"`
	}
	type resp struct {
		ID      string   `json:"id"`
		Object  string   `json:"object"`
		Choices []choice `json:"choices"`
		Usage   usage    `json:"usage"`
	}
	r := resp{
		ID:     "test",
		Object: "chat.completion",
		Choices: []choice{{
			Logprobs: logprobs{Content: []lp{{
				Token:   token,
				Logprob: logprob,
				TopLogprobs: []topLP{
					{Token: token, Logprob: logprob},
					{Token: "99", Logprob: logprob - 1.0},
				},
			}}},
		}},
		Usage: usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
		},
	}
	b, _ := json.Marshal(r)
	return b
}

// fakeHTTPResponse wraps a status code and body into a *http.Response.
func fakeHTTPResponse(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

// staticExecutor returns an execute func that always responds with the given status and body.
func staticExecutor(status int, body []byte) func(context.Context, []byte) (*http.Response, error) {
	return func(_ context.Context, _ []byte) (*http.Response, error) {
		return fakeHTTPResponse(status, body), nil
	}
}

var minimalPrompt = []byte(`{"messages":[]}`)

func TestExecuteValidation_InvalidPromptPayload(t *testing.T) {
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		[]byte("not-json"),
		responsePayloadJSON("42", -0.5),
		staticExecutor(200, responsePayloadJSON("42", -0.5)),
		0, 0, "processed_logprobs",
	)
	require.NoError(t, err)
	assert.IsType(t, &InvalidInferenceResult{}, result)
	assert.False(t, result.IsSuccessful())
}

func TestExecuteValidation_ExecuteError(t *testing.T) {
	exec := func(_ context.Context, _ []byte) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		responsePayloadJSON("42", -0.5),
		exec,
		0, 0, "processed_logprobs",
	)
	require.Error(t, err)
	assert.Nil(t, result)
}

func TestExecuteValidation_400Response_TreatedAsPass(t *testing.T) {
	// Mainnet parity: a 4xx from the validator's own re-execution is treated as
	// passed (warn + autopass), not invalid. See inference_validation.go (~944).
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		responsePayloadJSON("42", -0.5),
		staticExecutor(http.StatusBadRequest, nil),
		0, 0, "processed_logprobs",
	)
	require.NoError(t, err)
	require.IsType(t, &SimilarityValidationResult{}, result)
	assert.True(t, result.IsSuccessful(), "validator re-exec 400 must autopass per mainnet 4xx semantics")
}

func TestExecuteValidation_422Response_TreatedAsPass(t *testing.T) {
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		responsePayloadJSON("42", -0.5),
		staticExecutor(http.StatusUnprocessableEntity, nil),
		0, 0, "processed_logprobs",
	)
	require.NoError(t, err)
	require.IsType(t, &SimilarityValidationResult{}, result)
	assert.True(t, result.IsSuccessful(), "validator re-exec 422 must autopass per mainnet 4xx semantics")
}

func TestExecuteValidation_NonNumericTokens_ReturnsInvalid(t *testing.T) {
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		responsePayloadJSON("hello", -0.5), // non-numeric token
		staticExecutor(200, responsePayloadJSON("42", -0.5)),
		0, 0, "processed_logprobs",
	)
	require.NoError(t, err)
	assert.IsType(t, &InvalidInferenceResult{}, result)
	assert.False(t, result.IsSuccessful())
}

func TestExecuteValidation_EmptySentinel_ExecutorServes200_ReturnsInvalid(t *testing.T) {
	// Executor returned <EMPTY> originally but validator can serve the prompt — invalid.
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		responsePayloadJSON("<EMPTY>", -0.5),
		staticExecutor(http.StatusOK, responsePayloadJSON("42", -0.5)),
		0, 0, "processed_logprobs",
	)
	require.NoError(t, err)
	assert.IsType(t, &InvalidInferenceResult{}, result)
	assert.False(t, result.IsSuccessful())
}

func TestExecuteValidation_EmptySentinel_DropsEnforcedTokens(t *testing.T) {
	var capturedBody []byte
	exec := func(_ context.Context, body []byte) (*http.Response, error) {
		capturedBody = body
		return fakeHTTPResponse(http.StatusBadRequest, nil), nil
	}
	_, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		responsePayloadJSON("<EMPTY>", -0.5),
		exec,
		0, 0, "processed_logprobs",
	)
	require.NoError(t, err)

	var requestMap map[string]interface{}
	require.NoError(t, json.Unmarshal(capturedBody, &requestMap))
	assert.NotContains(t, requestMap, "enforced_tokens")
}

func TestExecuteValidation_NormalPath_SetsEnforcedTokensAndStream(t *testing.T) {
	var capturedBody []byte
	exec := func(_ context.Context, body []byte) (*http.Response, error) {
		capturedBody = body
		return fakeHTTPResponse(http.StatusOK, responsePayloadJSON("42", -0.5)), nil
	}
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		responsePayloadJSON("42", -0.5),
		exec,
		0, 0, "processed_logprobs",
	)
	require.NoError(t, err)
	assert.True(t, result.IsSuccessful())

	var requestMap map[string]interface{}
	require.NoError(t, json.Unmarshal(capturedBody, &requestMap))
	assert.Contains(t, requestMap, "enforced_tokens")
	assert.Equal(t, false, requestMap["stream"])
	assert.NotContains(t, requestMap, "stream_options")
	assert.Equal(t, true, requestMap["logprobs"])
	assert.Equal(t, float64(5), requestMap["top_logprobs"])
	assert.Equal(t, "processed_logprobs", requestMap["logprobs_mode"])
	assert.Equal(t, float64(calculations.DefaultMaxTokens), requestMap["max_tokens"])
	assert.Equal(t, float64(calculations.DefaultMaxTokens), requestMap["max_completion_tokens"])
	assert.Equal(t, float64(0), requestMap["seed"])
}

func TestExecuteValidation_MatchingLogits_PassesSimilarityThreshold(t *testing.T) {
	payload := responsePayloadJSON("42", -0.5)
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		payload,
		staticExecutor(http.StatusOK, payload), // identical response → similarity 1.0
		0, 0, "processed_logprobs",
	)
	require.NoError(t, err)
	require.IsType(t, &SimilarityValidationResult{}, result)
	assert.True(t, result.IsSuccessful())
}

func emptyLogprobsResponsePayload() []byte {
	b, _ := json.Marshal(map[string]interface{}{
		"id":     "test",
		"object": "chat.completion",
		"choices": []map[string]interface{}{{
			"index":    0,
			"logprobs": map[string]interface{}{"content": []interface{}{}},
		}},
	})
	return b
}

func TestExecuteValidation_EmptyOriginalLogits_IsInvalid(t *testing.T) {
	// Executor stored no logprobs but validator re-exec has logits: asymmetric
	// fail-open closed. Unpatched CompareLogits([], x) would return similarity 1.0.
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		emptyLogprobsResponsePayload(),
		staticExecutor(http.StatusOK, responsePayloadJSON("42", -0.5)),
		0, 0, "processed_logprobs",
	)
	require.NoError(t, err)
	require.IsType(t, &InvalidInferenceResult{}, result)
	assert.False(t, result.IsSuccessful(), "executor response with no logprobs must be rejected")
}

func TestExecuteValidation_NoLogitsInValidatorResponse_IsInvalid(t *testing.T) {
	// Validator returns a response with no logprobs content while original has logits.
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		responsePayloadJSON("42", -0.5),
		staticExecutor(http.StatusOK, emptyLogprobsResponsePayload()),
		0, 0, "processed_logprobs",
	)
	require.NoError(t, err)
	require.IsType(t, &InvalidInferenceResult{}, result)
	assert.False(t, result.IsSuccessful())
}

func TestExecuteValidation_BothEmptyLogits_StaysValid(t *testing.T) {
	// Legitimate reasoning-burn empties (both sides empty) must remain a match
	// (warn + autopass). Deliberately looser than mainnet's || fail-closed guard.
	empty := emptyLogprobsResponsePayload()
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		empty,
		staticExecutor(http.StatusOK, empty),
		0, 0, "processed_logprobs",
	)
	require.NoError(t, err)
	require.IsType(t, &SimilarityValidationResult{}, result)
	assert.True(t, result.IsSuccessful(), "legitimate both-empty must remain valid")
}

func TestExecuteValidation_TokenInflationWithinTolerance_Passes(t *testing.T) {
	// Claimed output is 3 tokens above validation replay — within ±3 tolerance.
	validatorResponse := responsePayloadJSONWithUsage("42", -0.5, 100, 100)
	original := responsePayloadJSON("42", -0.5)
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		original,
		staticExecutor(http.StatusOK, validatorResponse),
		100, 103, "processed_logprobs",
	)
	require.NoError(t, err)
	assert.True(t, result.IsSuccessful())
}

func TestExecuteValidation_TokenInflationAboveTolerance_Fails(t *testing.T) {
	validatorResponse := responsePayloadJSONWithUsage("42", -0.5, 100, 100)
	original := responsePayloadJSON("42", -0.5)
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		original,
		staticExecutor(http.StatusOK, validatorResponse),
		100, 104, "processed_logprobs",
	)
	require.NoError(t, err)
	assert.IsType(t, &InvalidInferenceResult{}, result)
	assert.False(t, result.IsSuccessful())
}

func TestExecuteValidation_DeepSeekInputUsageException(t *testing.T) {
	for _, tc := range []struct {
		name          string
		model         string
		input, output uint64
		history       bool
		wrongToken    bool
		valid         bool
		exception     bool
		suspected     bool
	}{
		{name: "prefix 78", input: 178, output: 100, valid: true, exception: true},
		{name: "prefix 79", input: 179, output: 100, valid: true, exception: true},
		{name: "below exception", input: 177, output: 100},
		{name: "above exception", input: 180, output: 100},
		{name: "normal tolerance", input: 103, output: 100, valid: true},
		{name: "reverse direction", input: 21, output: 100, valid: true},
		{name: "other model", model: "other-model", input: 179, output: 100},
		{name: "output still checked", input: 179, output: 104, exception: true},
		{name: "logits still checked", input: 179, output: 100, wrongToken: true, exception: true},
		{name: "history suspected", input: 60468, output: 100, history: true, suspected: true},
		{name: "large delta without history", input: 60468, output: 100},
		{name: "history with output mismatch", input: 60468, output: 104, history: true},
		{name: "other model with history", model: "other-model", input: 60468, output: 100, history: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			model := tc.model
			if model == "" {
				model = "deepseek-ai/DeepSeek-V4-Flash-0731"
			}
			messages := []map[string]interface{}{{"role": "user", "content": "hello"}}
			if tc.history {
				messages = append(messages, map[string]interface{}{"role": "assistant", "content": "answer", "reasoning_content": "previous reasoning"})
				messages = append(messages, map[string]interface{}{"role": "user", "content": "continue"})
			}
			prompt, err := json.Marshal(map[string]interface{}{"model": model, "messages": messages})
			require.NoError(t, err)
			token := "42"
			if tc.wrongToken {
				token = "43"
			}
			calls := 0
			execute := staticExecutor(http.StatusOK, responsePayloadJSONWithUsage(token, -0.5, 100, 100))
			result, err := ExecuteValidation(context.Background(), "inf-1", prompt, responsePayloadJSON("42", -0.5),
				func(ctx context.Context, body []byte) (*http.Response, error) {
					calls++
					return execute(ctx, body)
				}, tc.input, tc.output, "processed_logprobs")
			require.NoError(t, err)
			assert.Equal(t, tc.valid, result.IsSuccessful())
			assert.Equal(t, 1, calls)
			assert.Equal(t, tc.exception, bytes.Contains(logs.Bytes(), []byte("exception=deepseek_formatter_prefix")))
			assert.Equal(t, tc.suspected, bytes.Contains(logs.Bytes(), []byte("suspected_cause=deepseek_formatter_history")))
			if tc.suspected {
				assert.IsType(t, &InvalidInferenceResult{}, result)
				assert.Contains(t, logs.String(), "resolution=invalid")
			}
		})
	}
}
