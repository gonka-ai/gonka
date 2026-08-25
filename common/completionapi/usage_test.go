package completionapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

const cachedUsageJSON = `{
  "id": "cmpl-cached-usage-001",
  "object": "chat.completion",
  "created": 1722197602,
  "model": "Qwen/Qwen2.5-7B-Instruct",
  "choices": [
    {
      "index": 0,
      "message": { "role": "assistant", "content": "ok" },
      "logprobs": null,
      "finish_reason": "stop",
      "stop_reason": null
    }
  ],
  "usage": {
    "prompt_tokens": 48025,
    "completion_tokens": 16,
    "total_tokens": 48041,
    "prompt_tokens_details": {
      "cached_tokens": 48003,
      "uncached_tokens": 4024
    }
  }
}`

func TestUsage_ParsesProviderCacheMetadata(t *testing.T) {
	resp, err := NewCompletionResponseFromBytes([]byte(cachedUsageJSON))
	require.NoError(t, err)

	usage, err := resp.GetUsage()
	require.NoError(t, err)
	require.Equal(t, uint64(48025), usage.PromptTokens)
	require.Equal(t, uint64(16), usage.CompletionTokens)
	require.Equal(t, uint64(48041), usage.TotalTokens)
	require.NotNil(t, usage.PromptTokensDetails)
	require.Equal(t, uint64(48003), usage.PromptTokensDetails.CachedTokens)
	require.Equal(t, uint64(4024), usage.PromptTokensDetails.UncachedTokens)
}

func TestUsage_RoundTripPreservesCacheMetadata(t *testing.T) {
	resp, err := NewCompletionResponseFromBytes([]byte(cachedUsageJSON))
	require.NoError(t, err)

	usage, err := resp.GetUsage()
	require.NoError(t, err)

	reMarshaled, err := json.Marshal(usage)
	require.NoError(t, err)

	var reparsed Usage
	require.NoError(t, json.Unmarshal(reMarshaled, &reparsed))
	require.NotNil(t, reparsed.PromptTokensDetails)
	require.Equal(t, uint64(48003), reparsed.PromptTokensDetails.CachedTokens)
	require.Equal(t, uint64(48041), reparsed.TotalTokens)
}

func TestUsage_NullPromptTokensDetailsIsNotAnError(t *testing.T) {
	nullDetails := `{
  "id": "cmpl-null-details",
  "object": "chat.completion",
  "created": 1722197602,
  "model": "Qwen/Qwen2.5-7B-Instruct",
  "choices": [
    {
      "index": 0,
      "message": { "role": "assistant", "content": "ok" },
      "logprobs": null,
      "finish_reason": "stop",
      "stop_reason": null
    }
  ],
  "usage": {
    "prompt_tokens": 100,
    "completion_tokens": 10,
    "total_tokens": 110,
    "prompt_tokens_details": null
  }
}`
	resp, err := NewCompletionResponseFromBytes([]byte(nullDetails))
	require.NoError(t, err)

	usage, err := resp.GetUsage()
	require.NoError(t, err)
	require.Nil(t, usage.PromptTokensDetails)
	require.Equal(t, uint64(100), usage.PromptTokens)
	require.False(t, usage.IsEmpty())
}

func TestUsage_StreamedResponseCarriesCacheMetadata(t *testing.T) {
	lines := []string{
		`data: {"id":"cmpl-stream","object":"chat.completion.chunk","created":1722197602,"model":"Qwen/Qwen2.5-7B-Instruct","choices":[{"index":0,"delta":{"content":"ok"},"logprobs":null,"finish_reason":null}]}`,
		`data: {"id":"cmpl-stream","object":"chat.completion.chunk","created":1722197602,"model":"Qwen/Qwen2.5-7B-Instruct","choices":[{"index":0,"delta":{},"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":48025,"completion_tokens":16,"total_tokens":48041,"prompt_tokens_details":{"cached_tokens":48003,"uncached_tokens":4024}}}`,
		`data: [DONE]`,
	}
	resp, err := NewCompletionResponseFromLines(lines)
	require.NoError(t, err)

	usage, err := resp.GetUsage()
	require.NoError(t, err)
	require.NotNil(t, usage.PromptTokensDetails)
	require.Equal(t, uint64(48003), usage.PromptTokensDetails.CachedTokens)
}