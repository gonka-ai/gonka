package completionapi

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStreamedGetUsageRequiresUsageChunk(t *testing.T) {
	withUsage := []string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"logprobs":{"content":[{"token":"Hi","logprob":0}]},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"m","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":1}}`,
		`data: [DONE]`,
	}
	resp, err := NewCompletionResponseFromLines(withUsage)
	require.NoError(t, err)

	usage, err := resp.GetUsage()
	require.NoError(t, err)
	require.Equal(t, &Usage{PromptTokens: 12, CompletionTokens: 1}, usage)

	estimated, err := resp.(*StreamedCompletionResponse).GetUsageOrEstimate()
	require.NoError(t, err)
	require.Equal(t, usage, estimated)
}

func TestStreamedGetUsageMissingChunkReturnsSentinel(t *testing.T) {
	withoutUsage := []string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"logprobs":{"content":[{"token":"Hi","logprob":0},{"token":"!","logprob":0}]},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}
	resp, err := NewCompletionResponseFromLines(withoutUsage)
	require.NoError(t, err)

	usage, err := resp.GetUsage()
	require.Nil(t, usage)
	require.ErrorIs(t, err, ErrStreamedUsageMissing)

	estimated, err := resp.(*StreamedCompletionResponse).GetUsageOrEstimate()
	require.NoError(t, err)
	require.Equal(t, &Usage{PromptTokens: 0, CompletionTokens: 2}, estimated)
}

func TestStreamedGetUsageNoData(t *testing.T) {
	resp := &StreamedCompletionResponse{}
	usage, err := resp.GetUsage()
	require.Nil(t, usage)
	require.True(t, errors.Is(err, ErrorNoDataAvailableInStreamedResponse))

	estimated, err := resp.GetUsageOrEstimate()
	require.Nil(t, estimated)
	require.True(t, errors.Is(err, ErrorNoDataAvailableInStreamedResponse))
}
