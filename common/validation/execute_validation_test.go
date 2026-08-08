package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"common/completionapi"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExecuteValidation_ReplayRequestCarriesMinTokensFloor proves the validator's replay request
// still carries the min_tokens floor via ModifyRequestBodyWithLogprobsMode, so the standalone
// EnforceTokenBudgetFloor call is redundant.
func TestExecuteValidation_ReplayRequestCarriesMinTokensFloor(t *testing.T) {
	promptPayload := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`)
	responsePayload := responsePayloadJSON("42", -0.1)

	var captured map[string]interface{}
	execute := func(ctx context.Context, body []byte) (*http.Response, error) {
		require.NoError(t, json.Unmarshal(body, &captured))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(responsePayload))}, nil
	}

	_, err := ExecuteValidation(context.Background(), "inf-1", promptPayload, responsePayload, execute, 0, 0, "")
	require.NoError(t, err)
	require.EqualValues(t, completionapi.MinTokensFloor, captured["min_tokens"])
	require.EqualValues(t, completionapi.MinTokensFloor, captured["max_tokens"])
}

// responsePayloadTokens builds a completion response with `count` output tokens and the given
// finish_reason / stop_reason, for exercising the min_tokens output-length check.
func responsePayloadTokens(count int, finishReason, stopReason string) []byte {
	type topLP struct {
		Token   string  `json:"token"`
		Logprob float64 `json:"logprob"`
	}
	type lp struct {
		Token       string  `json:"token"`
		Logprob     float64 `json:"logprob"`
		TopLogprobs []topLP `json:"top_logprobs"`
	}
	content := make([]lp, 0, count)
	for i := 0; i < count; i++ {
		content = append(content, lp{Token: "42", Logprob: -0.1, TopLogprobs: []topLP{{Token: "42", Logprob: -0.1}, {Token: "99", Logprob: -1.1}}})
	}
	r := map[string]interface{}{
		"id":     "test",
		"object": "chat.completion",
		"choices": []map[string]interface{}{{
			"index":         0,
			"finish_reason": finishReason,
			"stop_reason":   stopReason,
			"logprobs":      map[string]interface{}{"content": content},
		}},
	}
	b, _ := json.Marshal(r)
	return b
}

// responsePayloadTokensWithUsage is responsePayloadTokens plus a usage block, for the token-count
// (inflation) checks that read usage.completion_tokens.
func responsePayloadTokensWithUsage(count int, promptTokens, completionTokens uint64) []byte {
	type topLP struct {
		Token   string  `json:"token"`
		Logprob float64 `json:"logprob"`
	}
	type lp struct {
		Token       string  `json:"token"`
		Logprob     float64 `json:"logprob"`
		TopLogprobs []topLP `json:"top_logprobs"`
	}
	content := make([]lp, 0, count)
	for i := 0; i < count; i++ {
		content = append(content, lp{Token: "42", Logprob: -0.1, TopLogprobs: []topLP{{Token: "42", Logprob: -0.1}, {Token: "99", Logprob: -1.1}}})
	}
	r := map[string]interface{}{
		"id":      "test",
		"object":  "chat.completion",
		"choices": []map[string]interface{}{{"index": 0, "logprobs": map[string]interface{}{"content": content}}},
		"usage": map[string]interface{}{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}
	b, _ := json.Marshal(r)
	return b
}

// An executor that ignored min_tokens and emitted an early natural EOS (short output,
// finish_reason=stop, empty stop_reason) must be rejected.
func TestExecuteValidation_RejectsShortOutputThatIgnoredMinTokens(t *testing.T) {
	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":128}`)
	stored := responsePayloadTokens(10, "stop", "")

	execute := func(ctx context.Context, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(stored))}, nil
	}
	res, err := ExecuteValidation(context.Background(), "inf-short", prompt, stored, execute, 0, 0, "")
	require.NoError(t, err)
	_, invalid := res.(*InvalidInferenceResult)
	require.True(t, invalid, "short natural-EOS output below min_tokens must be invalid")
}

// A natural EOS at/above the floor is the normal case: the stop token can only fire after
// min_tokens, so a full-length response ending on a natural stop must NOT be flagged.
func TestExecuteValidation_AllowsFullLengthNaturalStop(t *testing.T) {
	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":128}`)
	stored := responsePayloadTokens(int(completionapi.MinTokensFloor), "stop", "") // exactly the floor

	execute := func(ctx context.Context, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(stored))}, nil
	}
	res, err := ExecuteValidation(context.Background(), "inf-full", prompt, stored, execute, 0, 0, "")
	require.NoError(t, err)
	_, invalid := res.(*InvalidInferenceResult)
	require.False(t, invalid, "a response of exactly min_tokens ending on natural EOS is valid")
}

// min_tokens masks stop-strings too, so an honest node cannot produce a short response even with a
// stop-string. A short output with stop_reason set is therefore still a floor violation.
func TestExecuteValidation_RejectsShortOutputEvenWithStopReason(t *testing.T) {
	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":128}`)
	stored := responsePayloadTokens(10, "stop", "\n\n") // stop-string set, but 10 < 64 floor

	execute := func(ctx context.Context, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(stored))}, nil
	}
	res, err := ExecuteValidation(context.Background(), "inf-stopstr", prompt, stored, execute, 0, 0, "")
	require.NoError(t, err)
	_, invalid := res.(*InvalidInferenceResult)
	require.True(t, invalid, "short output is a floor violation even with a stop_reason")
}

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
		return fakeHTTPResponse(http.StatusOK, responsePayloadTokens(64, "stop", "")), nil
	}
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		responsePayloadTokens(64, "stop", ""),
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
	payload := responsePayloadTokens(64, "stop", "")
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
	// 64-token responses keep the original above the min_tokens floor.
	validatorResponse := responsePayloadTokensWithUsage(64, 100, 100)
	original := responsePayloadTokens(64, "stop", "")
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
