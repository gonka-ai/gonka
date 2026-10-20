package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"common/completionapi"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Validate an output for a request with max_tokens 1 and capture the replay.
// 2. The replay keeps max_tokens 1 and carries no min_tokens.
func TestExecuteValidation_ReplayRequestKeepsShortMaxTokens(t *testing.T) {
	promptPayload := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`)
	responsePayload := responsePayloadJSON("42", -0.1)

	var captured map[string]interface{}
	execute := func(ctx context.Context, body []byte) (*http.Response, error) {
		require.NoError(t, json.Unmarshal(body, &captured))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(responsePayload))}, nil
	}

	_, err := ExecuteValidation(context.Background(), "inf-1", promptPayload, responsePayload, execute, 0, 0, "", 0)
	require.NoError(t, err)
	require.NotContains(t, captured, "min_tokens")
	require.EqualValues(t, 1, captured["max_tokens"])
}

// Test flow:
// 1. Build the executor body for a json_schema request with a caller min_tokens of 16.
// 2. Replay a stored 10-token output through ExecuteValidation and capture the replay body.
// 3. Neither body carries min_tokens, both carry the same max_tokens, and the output is compared, not rejected as short.
func TestExecuteValidation_GrammarConstrainedReplayMatchesExecution(t *testing.T) {
	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"min_tokens":16,"max_tokens":32,"response_format":{"type":"json_schema","json_schema":{"name":"a","schema":{"type":"object"}}}}`)
	stored := responsePayloadTokens(10, "stop", "")

	executed, err := completionapi.ModifyRequestBodyWithLogprobsMode(prompt, 1, "")
	require.NoError(t, err)
	var executedMap map[string]interface{}
	require.NoError(t, json.Unmarshal(executed.NewBody, &executedMap))

	var replayed map[string]interface{}
	execute := func(ctx context.Context, body []byte) (*http.Response, error) {
		require.NoError(t, json.Unmarshal(body, &replayed))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(stored))}, nil
	}
	res, err := ExecuteValidation(context.Background(), "inf-grammar", prompt, stored, execute, 0, 0, "", 0)
	require.NoError(t, err)

	require.NotContains(t, executedMap, "min_tokens")
	require.NotContains(t, replayed, "min_tokens")
	require.EqualValues(t, 32, executedMap["max_tokens"])
	require.Equal(t, executedMap["max_tokens"], replayed["max_tokens"])
	similarity, ok := res.(*SimilarityValidationResult)
	require.True(t, ok, "got %T %+v", res, res)
	require.Equal(t, 1.0, similarity.Value)
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

// Test flow:
// 1. Store a 10-token natural-EOS output for a request with min_tokens 64.
// 2. The validation is invalid.
func TestExecuteValidation_RejectsShortOutputThatIgnoredMinTokens(t *testing.T) {
	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":128,"min_tokens":64}`)
	stored := responsePayloadTokens(10, "stop", "")

	execute := func(ctx context.Context, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(stored))}, nil
	}
	res, err := ExecuteValidation(context.Background(), "inf-short", prompt, stored, execute, 0, 0, "", 0)
	require.NoError(t, err)
	_, invalid := res.(*InvalidInferenceResult)
	require.True(t, invalid, "short natural-EOS output below min_tokens must be invalid")
}

// Test flow:
// 1. Store a natural-EOS output of exactly min_tokens (64) tokens.
// 2. The validation is not invalid.
func TestExecuteValidation_AllowsFullLengthNaturalStop(t *testing.T) {
	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":128,"min_tokens":64}`)
	stored := responsePayloadTokens(64, "stop", "")

	execute := func(ctx context.Context, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(stored))}, nil
	}
	res, err := ExecuteValidation(context.Background(), "inf-full", prompt, stored, execute, 0, 0, "", 0)
	require.NoError(t, err)
	_, invalid := res.(*InvalidInferenceResult)
	require.False(t, invalid, "a response of exactly min_tokens ending on natural EOS is valid")
}

// Test flow:
// 1. Store a 10-token output that ends on a stop-string for a request with min_tokens 64.
// 2. The validation is invalid, since min_tokens also masks stop-strings.
func TestExecuteValidation_RejectsShortOutputEvenWithStopReason(t *testing.T) {
	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":128,"min_tokens":64}`)
	stored := responsePayloadTokens(10, "stop", "\n\n")

	execute := func(ctx context.Context, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(stored))}, nil
	}
	res, err := ExecuteValidation(context.Background(), "inf-stopstr", prompt, stored, execute, 0, 0, "", 0)
	require.NoError(t, err)
	_, invalid := res.(*InvalidInferenceResult)
	require.True(t, invalid, "short output is a min_tokens violation even with a stop_reason")
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
		0, 0, "processed_logprobs", 0,
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
		0, 0, "processed_logprobs", 0,
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
		0, 0, "processed_logprobs", 0,
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
		0, 0, "processed_logprobs", 0,
	)
	require.NoError(t, err)
	require.IsType(t, &SimilarityValidationResult{}, result)
	assert.True(t, result.IsSuccessful(), "validator re-exec 422 must autopass per mainnet 4xx semantics")
}

// Test flow:
// 1. The executor stores a token id below the replay cap, so the replay reaches the validator node.
// 2. The node answers 400 naming exactly enforced_tokens (its vocab check) and the validation is invalid.
// 3. A pydantic 400 whose param only nests enforced_tokens keeps the 4xx autopass.
func TestExecuteValidation_400EnforcedTokensParam(t *testing.T) {
	cases := []struct {
		name           string
		errorBody      []byte
		wantSuccessful bool
	}{
		{"vocab rejection is invalid", []byte(`{"error":{"message":"invalid enforced token at position 0: 151936 not in [0, 151936)","type":"BadRequestError","param":"enforced_tokens","code":400}}`), false},
		{"nested pydantic param autopasses", []byte(`{"error":{"message":"List should have at most 32768 items","type":"BadRequestError","param":"body.enforced_tokens.tokens","code":400}}`), true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			replayed := false
			execute := func(_ context.Context, _ []byte) (*http.Response, error) {
				replayed = true
				return fakeHTTPResponse(http.StatusBadRequest, testCase.errorBody), nil
			}
			result, err := ExecuteValidation(context.Background(), "inf-1", minimalPrompt, responsePayloadJSON("151936", -0.5), execute, 0, 0, "processed_logprobs", 0)
			require.NoError(t, err)
			assert.True(t, replayed, "the 400 must come from the validator node")
			assert.Equal(t, testCase.wantSuccessful, result.IsSuccessful())
		})
	}
}

// responsePayloadWithTopTokens builds a one-position response whose top_logprobs carry topTokenCount entries.
func responsePayloadWithTopTokens(token string, topTokenCount int) []byte {
	topLogprobs := make([]map[string]interface{}, 0, topTokenCount)
	for position := 0; position < topTokenCount; position++ {
		topLogprobs = append(topLogprobs, map[string]interface{}{"token": strconv.Itoa(position), "logprob": -1.0})
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"id":     "test",
		"object": "chat.completion",
		"choices": []map[string]interface{}{{
			"index": 0,
			"logprobs": map[string]interface{}{"content": []map[string]interface{}{{
				"token": token, "logprob": -0.5, "top_logprobs": topLogprobs,
			}}},
		}},
	})
	return payload
}

// Test flow:
// 1. The executor stores a token id at the replay cap or a position with more top tokens than the node accepts.
// 2. The validation is invalid and the replay never reaches the validator node.
func TestExecuteValidation_UnreplayableTokens_InvalidWithoutReplay(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{"token id at cap", responsePayloadWithTopTokens("9999999", 5)},
		{"too many top tokens", responsePayloadWithTopTokens("42", 65)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			replayed := false
			execute := func(_ context.Context, _ []byte) (*http.Response, error) {
				replayed = true
				return fakeHTTPResponse(http.StatusOK, testCase.payload), nil
			}
			result, err := ExecuteValidation(context.Background(), "inf-1", minimalPrompt, testCase.payload, execute, 0, 0, "processed_logprobs", 0)
			require.NoError(t, err)
			assert.False(t, replayed, "unreplayable tokens must not reach the validator node")
			require.IsType(t, &InvalidInferenceResult{}, result)
		})
	}
}

// Test flow:
// 1. The executor stores a token id just below the cap or exactly the node's top-token limit.
// 2. The replay is sent to the validator node.
func TestExecuteValidation_TokensWithinReplayLimits_Replayed(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{"token id below cap", responsePayloadWithTopTokens("9999998", 5)},
		{"top tokens at node limit", responsePayloadWithTopTokens("42", 64)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			replayed := false
			execute := func(_ context.Context, _ []byte) (*http.Response, error) {
				replayed = true
				return fakeHTTPResponse(http.StatusOK, testCase.payload), nil
			}
			_, err := ExecuteValidation(context.Background(), "inf-1", minimalPrompt, testCase.payload, execute, 0, 0, "processed_logprobs", 0)
			require.NoError(t, err)
			assert.True(t, replayed, "tokens within the limits must be replayed")
		})
	}
}

// Test flow:
// 1. The model's vocab size is known and the executor stores a token id equal to it (one past the last valid id).
// 2. The validation is invalid and the replay never reaches the validator node.
// 3. The last valid id (vocab size minus one) is replayed.
func TestExecuteValidation_KnownVocabulary_BoundsTokenIDs(t *testing.T) {
	const vocabularySize = 151936
	cases := []struct {
		name         string
		token        string
		wantReplayed bool
	}{
		{"id equal to vocab size is not replayed", "151936", false},
		{"last vocab id is replayed", "151935", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			payload := responsePayloadWithTopTokens(testCase.token, 5)
			replayed := false
			execute := func(_ context.Context, _ []byte) (*http.Response, error) {
				replayed = true
				return fakeHTTPResponse(http.StatusOK, payload), nil
			}
			_, err := ExecuteValidation(context.Background(), "inf-1", minimalPrompt, payload, execute, 0, 0, "processed_logprobs", vocabularySize)
			require.NoError(t, err)
			assert.Equal(t, testCase.wantReplayed, replayed)
		})
	}
}

// responsePayloadWithPositions builds a response whose logprobs carry positionCount positions.
func responsePayloadWithPositions(positionCount int) []byte {
	positions := make([]map[string]interface{}, 0, positionCount)
	for position := 0; position < positionCount; position++ {
		positions = append(positions, map[string]interface{}{
			"token": "42", "logprob": -0.5, "top_logprobs": []map[string]interface{}{{"token": "42", "logprob": -0.5}},
		})
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"id":      "test",
		"object":  "chat.completion",
		"choices": []map[string]interface{}{{"index": 0, "logprobs": map[string]interface{}{"content": positions}}},
	})
	return payload
}

// Test flow:
// 1. The executor stores more logprobs positions than the prompt's max_tokens.
// 2. The validation is invalid and the replay never reaches the validator node.
// 3. Up to that limit the output is replayed, including a short max_tokens.
func TestExecuteValidation_PositionsBoundedByMaxTokens(t *testing.T) {
	cases := []struct {
		name          string
		prompt        []byte
		positionCount int
		wantReplayed  bool
	}{
		{"padded past the node's list limit", []byte(`{"messages":[],"max_tokens":4096}`), 32769, false},
		{"one past max_tokens", []byte(`{"messages":[],"max_tokens":4096}`), 4097, false},
		{"exactly max_tokens", []byte(`{"messages":[],"max_tokens":4096}`), 4096, true},
		{"max_completion_tokens bounds too", []byte(`{"messages":[],"max_completion_tokens":100}`), 101, false},
		{"exactly a small max_tokens", []byte(`{"messages":[],"max_tokens":10}`), 10, true},
		{"one past a small max_tokens", []byte(`{"messages":[],"max_tokens":10}`), 11, false},
		{"any output for zero max_tokens", []byte(`{"messages":[],"max_tokens":0}`), 1, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			payload := responsePayloadWithPositions(testCase.positionCount)
			replayed := false
			execute := func(_ context.Context, _ []byte) (*http.Response, error) {
				replayed = true
				return fakeHTTPResponse(http.StatusOK, payload), nil
			}
			result, err := ExecuteValidation(context.Background(), "inf-1", testCase.prompt, payload, execute, 0, 0, "processed_logprobs", 0)
			require.NoError(t, err)
			assert.Equal(t, testCase.wantReplayed, replayed)
			if !testCase.wantReplayed {
				require.IsType(t, &InvalidInferenceResult{}, result)
			}
		})
	}
}

func TestExecuteValidation_NonNumericTokens_ReturnsInvalid(t *testing.T) {
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		responsePayloadJSON("hello", -0.5), // non-numeric token
		staticExecutor(200, responsePayloadJSON("42", -0.5)),
		0, 0, "processed_logprobs", 0,
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
		0, 0, "processed_logprobs", 0,
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
		0, 0, "processed_logprobs", 0,
	)
	require.NoError(t, err)

	var requestMap map[string]interface{}
	require.NoError(t, json.Unmarshal(capturedBody, &requestMap))
	assert.NotContains(t, requestMap, "enforced_tokens")
}

func TestExecuteValidation_NormalPath_SetsEnforcedTokensAndStream(t *testing.T) {
	n := int(completionapi.MinTokensFloor)
	var capturedBody []byte
	exec := func(_ context.Context, body []byte) (*http.Response, error) {
		capturedBody = body
		return fakeHTTPResponse(http.StatusOK, responsePayloadTokens(n, "stop", "")), nil
	}
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		responsePayloadTokens(n, "stop", ""),
		exec,
		0, 0, "processed_logprobs", 0,
	)
	require.NoError(t, err)
	assert.True(t, result.IsSuccessful())

	var requestMap map[string]interface{}
	require.NoError(t, json.Unmarshal(capturedBody, &requestMap))
	assert.Contains(t, requestMap, "enforced_tokens")
	assert.Equal(t, true, requestMap["stream"])
	assert.Equal(t, map[string]interface{}{"include_usage": true}, requestMap["stream_options"])
	assert.Equal(t, true, requestMap["logprobs"])
	assert.Equal(t, float64(5), requestMap["top_logprobs"])
	assert.Equal(t, "processed_logprobs", requestMap["logprobs_mode"])
	assert.Equal(t, float64(calculations.DefaultMaxTokens), requestMap["max_tokens"])
	assert.Equal(t, float64(calculations.DefaultMaxTokens), requestMap["max_completion_tokens"])
	assert.Equal(t, float64(0), requestMap["seed"])
}

func TestExecuteValidation_MatchingLogits_PassesSimilarityThreshold(t *testing.T) {
	payload := responsePayloadTokens(int(completionapi.MinTokensFloor), "stop", "")
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		payload,
		staticExecutor(http.StatusOK, payload), // identical response → similarity 1.0
		0, 0, "processed_logprobs", 0,
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
		0, 0, "processed_logprobs", 0,
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
		0, 0, "processed_logprobs", 0,
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
		0, 0, "processed_logprobs", 0,
	)
	require.NoError(t, err)
	require.IsType(t, &SimilarityValidationResult{}, result)
	assert.True(t, result.IsSuccessful(), "legitimate both-empty must remain valid")
}

func TestExecuteValidation_TokenInflationWithinTolerance_Passes(t *testing.T) {
	// Claimed output is 3 tokens above validation replay — within ±3 tolerance.
	// Floor-length responses keep the original at the min_tokens floor.
	n := int(completionapi.MinTokensFloor)
	validatorResponse := responsePayloadTokensWithUsage(n, 100, 100)
	original := responsePayloadTokens(n, "stop", "")
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		original,
		staticExecutor(http.StatusOK, validatorResponse),
		100, 103, "processed_logprobs", 0,
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
		100, 104, "processed_logprobs", 0,
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
			validatorPayload := responsePayloadTokensWithUsage(100, 100, 100)
			if tc.wrongToken {
				validatorPayload = bytes.ReplaceAll(validatorPayload, []byte(`"42"`), []byte(`"43"`))
			}
			calls := 0
			execute := staticExecutor(http.StatusOK, validatorPayload)
			result, err := ExecuteValidation(context.Background(), "inf-1", prompt, responsePayloadTokens(100, "length", ""),
				func(ctx context.Context, body []byte) (*http.Response, error) {
					calls++
					return execute(ctx, body)
				}, tc.input, tc.output, "processed_logprobs", 0)
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

type replayPosition struct {
	token   string
	logprob *float64
}

func floatPtr(v float64) *float64 { return &v }

func replayPositions(prefix []float64, nanTail int) []replayPosition {
	positions := make([]replayPosition, 0, len(prefix)+nanTail)
	for _, lp := range prefix {
		positions = append(positions, replayPosition{token: "42", logprob: floatPtr(lp)})
	}
	for i := 0; i < nanTail; i++ {
		positions = append(positions, replayPosition{token: "0"})
	}
	return positions
}

func replayChunk(position replayPosition) map[string]interface{} {
	return map[string]interface{}{
		"token":   position.token,
		"logprob": position.logprob,
		"top_logprobs": []map[string]interface{}{
			{"token": position.token, "logprob": position.logprob},
			{"token": "99", "logprob": position.logprob},
		},
	}
}

func streamedResponsePayload(positions []replayPosition) []byte {
	events := make([]string, 0, len(positions)+1)
	for _, position := range positions {
		chunk, _ := json.Marshal(map[string]interface{}{
			"id":      "test",
			"object":  "chat.completion.chunk",
			"choices": []map[string]interface{}{{"index": 0, "delta": map[string]interface{}{"content": "x"}, "logprobs": map[string]interface{}{"content": []interface{}{replayChunk(position)}}}},
		})
		events = append(events, "data: "+string(chunk))
	}
	events = append(events, "data: [DONE]")
	b, _ := json.Marshal(map[string]interface{}{"events": events})
	return b
}

func replaySSE(positions []replayPosition, withUsage, withDone bool, errorEvent string) []byte {
	var sb strings.Builder
	for _, position := range positions {
		chunk, _ := json.Marshal(map[string]interface{}{
			"id":      "replay",
			"object":  "chat.completion.chunk",
			"choices": []map[string]interface{}{{"index": 0, "delta": map[string]interface{}{"content": "x"}, "logprobs": map[string]interface{}{"content": []interface{}{replayChunk(position)}}}},
			"usage":   nil,
		})
		sb.WriteString("data: " + string(chunk) + "\n\n")
	}
	if errorEvent != "" {
		sb.WriteString("data: " + errorEvent + "\n\n")
	}
	if withUsage {
		sb.WriteString(`data: {"id":"replay","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":` + strconv.Itoa(len(positions)) + `,"total_tokens":` + strconv.Itoa(10+len(positions)) + "}}\n\n")
	}
	if withDone {
		sb.WriteString("data: [DONE]\n\n")
	}
	return []byte(sb.String())
}

func grammarPrefix(n int, logprob float64) []float64 {
	prefix := make([]float64, n)
	for i := range prefix {
		prefix[i] = logprob
	}
	return prefix
}

func TestExecuteValidation_StreamedReplayWithNaNTailMatchesHonestExecutor(t *testing.T) {
	positions := replayPositions(grammarPrefix(20, -0.1), int(completionapi.MinTokensFloor)-20)
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		streamedResponsePayload(positions),
		staticExecutor(http.StatusOK, replaySSE(positions, true, true, "")),
		10, uint64(len(positions)), "processed_logprobs", 0,
	)
	require.NoError(t, err)
	require.IsType(t, &SimilarityValidationResult{}, result)
	assert.InDelta(t, 1.0, result.(*SimilarityValidationResult).Value, 1e-9)
	assert.True(t, result.IsSuccessful())
}

func TestExecuteValidation_StreamedReplayWithNaNTailScoresForgedPrefix(t *testing.T) {
	tail := int(completionapi.MinTokensFloor) - 40
	executor := replayPositions(grammarPrefix(40, -0.1), tail)
	validator := replayPositions(grammarPrefix(40, -6.0), tail)
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		streamedResponsePayload(executor),
		staticExecutor(http.StatusOK, replaySSE(validator, true, true, "")),
		10, uint64(len(executor)), "processed_logprobs", 0,
	)
	require.NoError(t, err)
	assert.False(t, result.IsSuccessful())
}

func TestExecuteValidation_StreamedReplayFailuresAreErrors(t *testing.T) {
	positions := replayPositions(grammarPrefix(int(completionapi.MinTokensFloor), -0.1), 0)
	cases := map[string][]byte{
		"error event": replaySSE(positions[:3], true, true, `{"error":{"message":"engine dead","type":"InternalServerError","code":500}}`),
		"no done":     replaySSE(positions, true, false, ""),
		"no usage":    replaySSE(positions, false, true, ""),
		"malformed":   append([]byte("data: {\"choices\":[{\"index\":0,\n\n"), replaySSE(positions, true, true, "")...),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := ExecuteValidation(
				context.Background(), "inf-1",
				minimalPrompt,
				streamedResponsePayload(positions),
				staticExecutor(http.StatusOK, body),
				10, uint64(len(positions)), "processed_logprobs", 0,
			)
			require.Error(t, err)
			assert.Nil(t, result)
		})
	}
}

func TestExecuteValidation_StreamedReplayErrorEventIsNotAnEmptySentinelVerdict(t *testing.T) {
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		responsePayloadJSON("<EMPTY>", -0.5),
		staticExecutor(http.StatusOK, replaySSE(nil, false, true, `{"error":{"message":"engine dead","type":"InternalServerError","code":500}}`)),
		0, 0, "processed_logprobs", 0,
	)
	require.Error(t, err)
	assert.Nil(t, result)
}

func TestExecuteValidation_StreamedReplayErrorEventKeepsNonStreamVerdicts(t *testing.T) {
	positions := replayPositions(grammarPrefix(int(completionapi.MinTokensFloor), -0.1), 0)
	cases := []struct {
		name    string
		event   string
		invalid bool
	}{
		{"enforced tokens rejected", `{"error":{"message":"token out of vocab","type":"BadRequestError","param":"enforced_tokens","code":400}}`, true},
		{"enforced tokens rejected without code", `{"error":{"message":"token out of vocab","type":"BadRequestError","param":"enforced_tokens"}}`, true},
		{"enforced tokens rejected with null code", `{"error":{"message":"token out of vocab","type":"BadRequestError","param":"enforced_tokens","code":null}}`, true},
		{"enforced tokens rejected with text code", `{"error":{"message":"token out of vocab","type":"BadRequestError","param":"enforced_tokens","code":"invalid_request"}}`, true},
		{"flat enforced tokens rejected without code", `{"object":"error","message":"token out of vocab","type":"BadRequestError","param":"enforced_tokens"}`, true},
		{"nested 400 autopass", `{"error":{"message":"bad","type":"BadRequestError","param":null,"code":400}}`, false},
		{"flat 422 autopass", `{"object":"error","message":"bad","type":"BadRequestError","param":null,"code":"422"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ExecuteValidation(
				context.Background(), "inf-1",
				minimalPrompt,
				streamedResponsePayload(positions),
				staticExecutor(http.StatusOK, replaySSE(positions[:2], false, true, tc.event)),
				10, uint64(len(positions)), "processed_logprobs", 0,
			)
			require.NoError(t, err)
			if tc.invalid {
				require.IsType(t, &InvalidInferenceResult{}, result)
				return
			}
			require.IsType(t, &SimilarityValidationResult{}, result)
			assert.True(t, result.IsSuccessful())
		})
	}
}

func TestExecuteValidation_StreamedReplayIgnoresSSECommentsAndFields(t *testing.T) {
	positions := replayPositions(grammarPrefix(int(completionapi.MinTokensFloor), -0.1), 4)
	events := strings.Split(strings.TrimSpace(string(replaySSE(positions, true, true, ""))), "\n\n")
	var sb strings.Builder
	sb.WriteString(": keepalive\r\n\r\n")
	for i, event := range events {
		if i == 3 {
			sb.WriteString(": ping\n\nevent: message\nid: 3\n")
		}
		sb.WriteString(event + "\r\n\r\n")
	}
	sb.WriteString(": trailing\n\n")
	result, err := ExecuteValidation(
		context.Background(), "inf-1",
		minimalPrompt,
		streamedResponsePayload(positions),
		staticExecutor(http.StatusOK, []byte(sb.String())),
		10, uint64(len(positions)), "processed_logprobs", 0,
	)
	require.NoError(t, err)
	require.IsType(t, &SimilarityValidationResult{}, result)
	assert.InDelta(t, 1.0, result.(*SimilarityValidationResult).Value, 1e-9)
}

func TestNormalizeReplayStream_NonStreamBodiesPassThrough(t *testing.T) {
	jsonBody := responsePayloadJSON("42", -0.5)
	status, body, err := NormalizeReplayStream(http.StatusOK, jsonBody)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, jsonBody, body)

	errorBody := []byte(`data: {"error":{"code":500}}`)
	status, body, err = NormalizeReplayStream(http.StatusBadRequest, errorBody)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, errorBody, body)
}
