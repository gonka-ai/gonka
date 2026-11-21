package validation

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// finishChunk is the chunk vLLM streams when a request ends, carrying only the finish_reason.
func finishChunk(finishReason string) string {
	return `{"id":"replay","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"logprobs":null,"finish_reason":"` + finishReason + `"}],"usage":null}`
}

// Test flow:
// 1. The executor claims 100 output tokens; the validator's complete replay stream has 2.
// 2. Ended with "abort" (vLLM dropped the request at PoC start): ErrReplayAborted, no result.
// 3. The same short replay ended with "stop" or "length" is still "Inflated token counts.".
func TestExecuteValidation_AbortedShortReplayIsDeferred(t *testing.T) {
	positions := replayPositions([]float64{-0.5, -0.5}, 0)
	for _, tc := range []struct {
		finishReason string
		wantDeferred bool
	}{
		{finishReason: "abort", wantDeferred: true},
		{finishReason: "stop"},
		{finishReason: "length"},
	} {
		t.Run(tc.finishReason, func(t *testing.T) {
			replay := replaySSE(positions, true, true, finishChunk(tc.finishReason))
			result, err := ExecuteValidation(
				context.Background(), "710",
				minimalPrompt,
				responsePayloadJSON("42", -0.5),
				staticExecutor(http.StatusOK, replay),
				10, 100, "processed_logprobs", 0,
			)
			if tc.wantDeferred {
				require.ErrorIs(t, err, ErrReplayAborted)
				assert.Contains(t, err.Error(), "inference 710")
				assert.Nil(t, result)
				return
			}
			require.NoError(t, err)
			invalid, ok := result.(*InvalidInferenceResult)
			require.True(t, ok, "got %T", result)
			assert.Equal(t, "Inflated token counts.", invalid.Reason)
		})
	}
}

// Test flow:
// 1. The executor stored the empty sentinel, so the replay runs without enforced tokens.
// 2. A replay aborted before its first token is deferred, not "prompt is servable".
// 3. A replay that the node finished ("stop") keeps that invalid verdict.
func TestExecuteValidation_AbortedEmptySentinelReplayIsDeferred(t *testing.T) {
	aborted := replaySSE(nil, true, true, finishChunk("abort"))
	result, err := ExecuteValidation(
		context.Background(), "711",
		minimalPrompt,
		responsePayloadJSON("<EMPTY>", -0.5),
		staticExecutor(http.StatusOK, aborted),
		0, 0, "processed_logprobs", 0,
	)
	require.ErrorIs(t, err, ErrReplayAborted)
	assert.Nil(t, result)

	served := replaySSE(replayPositions([]float64{-0.5}, 0), true, true, finishChunk("stop"))
	result, err = ExecuteValidation(
		context.Background(), "711",
		minimalPrompt,
		responsePayloadJSON("<EMPTY>", -0.5),
		staticExecutor(http.StatusOK, served),
		0, 0, "processed_logprobs", 0,
	)
	require.NoError(t, err)
	invalid, ok := result.(*InvalidInferenceResult)
	require.True(t, ok, "got %T", result)
	assert.Equal(t, "Executor returned error but prompt is servable.", invalid.Reason)
}

// A non-streamed replay body with an aborted choice is deferred the same way.
func TestExecuteValidation_AbortedNonStreamReplayIsDeferred(t *testing.T) {
	replay := []byte(`{"id":"replay","object":"chat.completion","choices":[{"index":0,"finish_reason":"abort",` +
		`"logprobs":{"content":[{"token":"42","logprob":-0.5,"top_logprobs":[{"token":"42","logprob":-0.5}]}]}}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`)
	result, err := ExecuteValidation(
		context.Background(), "712",
		minimalPrompt,
		responsePayloadJSON("42", -0.5),
		staticExecutor(http.StatusOK, replay),
		10, 100, "processed_logprobs", 0,
	)
	require.ErrorIs(t, err, ErrReplayAborted)
	assert.Nil(t, result)
}

func TestReplayFinishReasons(t *testing.T) {
	streamed, err := parseReplayResponse(replaySSE(replayPositions([]float64{-0.5}, 0), true, true, finishChunk("abort")))
	require.NoError(t, err)
	assert.Equal(t, []string{"abort"}, replayFinishReasons(streamed))

	plain, err := parseReplayResponse([]byte(`{"choices":[{"index":0,"finish_reason":"stop"},{"index":1,"finish_reason":"length"}]}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"stop", "length"}, replayFinishReasons(plain))

	noReason, err := parseReplayResponse(replaySSE(replayPositions([]float64{-0.5}, 0), true, true, ""))
	require.NoError(t, err)
	assert.Empty(t, replayFinishReasons(noReason))
}
