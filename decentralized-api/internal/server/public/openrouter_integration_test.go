package public

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestOpenRouterCompletionsFormat_NonStream(t *testing.T) {
	mockChatResponse := `{
		"id": "chatcmpl-abc123",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "qwen/qwen3-32b",
		"choices": [{
			"index": 0,
			"message": {"role": "assistant", "content": "The weather is sunny today."},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 12,
			"completion_tokens": 8,
			"total_tokens": 20
		}
	}`

	result, err := transformChatToCompletionResponse([]byte(mockChatResponse))
	require.NoError(t, err)

	jsonBytes, err := json.MarshalIndent(result, "", "  ")
	require.NoError(t, err)

	t.Logf("OpenRouter /completions response format:\n%s", string(jsonBytes))

	require.Equal(t, "text_completion", result.Object)
	require.Equal(t, "chatcmpl-abc123", result.ID)
	require.Equal(t, "qwen/qwen3-32b", result.Model)
	require.Len(t, result.Choices, 1)
	require.Equal(t, "The weather is sunny today.", result.Choices[0].Text)
	require.Equal(t, "stop", result.Choices[0].FinishReason)
	require.NotNil(t, result.Usage)
	require.Equal(t, 12, result.Usage.PromptTokens)
	require.Equal(t, 8, result.Usage.CompletionTokens)
	require.Equal(t, 20, result.Usage.TotalTokens)
}

func TestOpenRouterCompletionsFormat_Stream(t *testing.T) {
	chunks := []string{
		`{"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1700000000,"model":"qwen/qwen3-32b","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1700000000,"model":"qwen/qwen3-32b","choices":[{"index":0,"delta":{"content":"The "},"finish_reason":null}]}`,
		`{"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1700000000,"model":"qwen/qwen3-32b","choices":[{"index":0,"delta":{"content":"weather"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1700000000,"model":"qwen/qwen3-32b","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1700000000,"model":"qwen/qwen3-32b","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}`,
	}

	t.Log("OpenRouter /completions streaming format:")
	for i, chunk := range chunks {
		result, err := transformChatChunkToCompletionChunk(chunk)
		require.NoError(t, err)

		var parsed CompletionChunk
		require.NoError(t, json.Unmarshal([]byte(result), &parsed))

		t.Logf("Chunk %d: %s", i+1, result)
		require.Equal(t, "text_completion", parsed.Object)
	}

	lastResult, _ := transformChatChunkToCompletionChunk(chunks[len(chunks)-1])
	var lastChunk CompletionChunk
	require.NoError(t, json.Unmarshal([]byte(lastResult), &lastChunk))
	require.NotNil(t, lastChunk.Usage)
	require.Equal(t, 20, lastChunk.Usage.TotalTokens)
}

func TestOpenRouterCompletionsRequest_PromptToMessages(t *testing.T) {
	e := echo.New()

	requestBody := `{"model": "qwen/qwen3-32b", "prompt": "What is the weather?", "max_tokens": 100}`
	req := httptest.NewRequest(http.MethodPost, "/openrouter/api/v1/completions", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")

	var completionsReq OpenRouterCompletionsRequest
	require.NoError(t, json.Unmarshal([]byte(requestBody), &completionsReq))

	chatReq := transformCompletionsToChatRequest(&completionsReq)

	t.Logf("Input (completions): %s", requestBody)
	chatJSON, _ := json.MarshalIndent(chatReq, "", "  ")
	t.Logf("Transformed to chat:\n%s", string(chatJSON))

	messages := chatReq["messages"].([]map[string]string)
	require.Equal(t, "user", messages[0]["role"])
	require.Equal(t, "What is the weather?", messages[0]["content"])

	_ = e
}
