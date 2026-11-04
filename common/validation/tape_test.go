package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func tapeReplayBody(steps, completionTokens int) []byte {
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"logprobs": map[string]any{"content": []map[string]any{{
				"token": "42", "logprob": -0.1, "top_logprobs": []map[string]any{{"token": "42", "logprob": -0.1}},
			}}},
		}},
		"usage":      map[string]any{"prompt_tokens": 1, "completion_tokens": completionTokens},
		"gonka_tape": map[string]any{"steps": steps, "count": 0, "max_gap": 0, "sum_gap": 0},
	})
	return b
}

func tapeExecutor(t *testing.T, status func(req map[string]any) (int, []byte)) func(context.Context, []byte) (*http.Response, error) {
	return func(_ context.Context, body []byte) (*http.Response, error) {
		var req map[string]any
		require.NoError(t, json.Unmarshal(body, &req))
		code, resp := status(req)
		return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(resp))}, nil
	}
}

func runTape(t *testing.T, committed int, execute func(context.Context, []byte) (*http.Response, error)) ValidationResult {
	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":1000}`)
	result, err := ExecuteTapeValidation(context.Background(), "inf-1", prompt,
		responsePayloadTokens(committed, "stop", ""), execute, 0, 0, "")
	require.NoError(t, err)
	return result
}

func TestTape_ReplayOfTheSameLength_IsScored(t *testing.T) {
	result := runTape(t, 100, tapeExecutor(t, func(map[string]any) (int, []byte) {
		return http.StatusOK, tapeReplayBody(100, 100)
	}))
	require.IsType(t, &TapeValidationResult{}, result)
	require.True(t, result.IsSuccessful())
}

// The model goes on where the executor said it stopped: the output was cut.
func TestTape_ReplayPastTheCommittedTokens_Fails(t *testing.T) {
	result := runTape(t, 100, tapeExecutor(t, func(map[string]any) (int, []byte) {
		return http.StatusOK, tapeReplayBody(100, 101)
	}))
	require.IsType(t, &DifferentLengthValidationResult{}, result)
}

func TestTape_ReplayEndingEarly_Fails(t *testing.T) {
	result := runTape(t, 100, tapeExecutor(t, func(map[string]any) (int, []byte) {
		return http.StatusOK, tapeReplayBody(30, 30)
	}))
	require.IsType(t, &DifferentLengthValidationResult{}, result)
}

// Anything the executor put in its tokens to get them rejected (out of
// vocabulary, too many, malformed) must not buy the 4xx autopass.
func TestTape_RejectedTokensOfAServablePrompt_Fail(t *testing.T) {
	var probe map[string]any
	result := runTape(t, 100, tapeExecutor(t, func(req map[string]any) (int, []byte) {
		if _, replay := req["enforced_tokens"]; replay {
			return http.StatusBadRequest, []byte(`{"error":{"message":"no"}}`)
		}
		probe = req
		return http.StatusOK, tapeReplayBody(1, 1)
	}))
	require.IsType(t, &InvalidInferenceResult{}, result)
	require.False(t, result.IsSuccessful())
	require.EqualValues(t, 1, probe["max_tokens"])
	require.NotContains(t, probe, "min_tokens")
}

func TestTape_PromptTheValidatorCannotServe_StaysAutopass(t *testing.T) {
	result := runTape(t, 100, tapeExecutor(t, func(map[string]any) (int, []byte) {
		return http.StatusBadRequest, []byte(`{"error":{"message":"prompt too long"}}`)
	}))
	require.IsType(t, &SimilarityValidationResult{}, result)
	require.True(t, result.IsSuccessful())
}

func TestTape_BeamSearchIsDroppedFromTheReplay(t *testing.T) {
	prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":1000,"use_beam_search":true}`)
	var replay map[string]any
	execute := tapeExecutor(t, func(req map[string]any) (int, []byte) {
		replay = req
		return http.StatusOK, tapeReplayBody(100, 100)
	})
	_, err := ExecuteTapeValidation(context.Background(), "inf-1", prompt,
		responsePayloadTokens(100, "stop", ""), execute, 0, 0, "")
	require.NoError(t, err)
	require.NotContains(t, replay, "use_beam_search")
}
