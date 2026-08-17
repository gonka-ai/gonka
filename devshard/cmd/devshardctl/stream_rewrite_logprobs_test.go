package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// parseSSEChunks extracts the JSON data events (excluding [DONE]) from an SSE
// byte stream so tests can assert on the synthesized chunk objects.
func parseSSEChunks(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}
		var evt map[string]any
		require.NoError(t, json.Unmarshal([]byte(data), &evt))
		events = append(events, evt)
	}
	return events
}

// Route A (filterClientInternalFields): when the client asked for logprobs, the
// logprobs/top_logprobs payloads survive, but the always-internal fields
// (token_ids, prompt_token_ids, prompt_logprobs) are still stripped.
func TestFilterClientInternalFields_KeepsLogprobsWhenRequested(t *testing.T) {
	payload := []byte(`{"prompt_token_ids":[1,2],"choices":[{"message":{"content":"Hi"},"token_ids":[3,4],"logprobs":{"content":[{"token":"Hi","logprob":-0.1,"top_logprobs":[{"token":"Hi","logprob":-0.1}]}]}}]}`)

	filtered := filterClientInternalFields(payload, logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})

	require.JSONEq(t, `{"choices":[{"message":{"content":"Hi"},"logprobs":{"content":[{"token":"Hi","logprob":-0.1,"top_logprobs":[{"token":"Hi","logprob":-0.1}]}]}}]}`, string(filtered))
}

// Route A: logprobs requested but top_logprobs not -- keep logprobs/per-token
// logprob, but empty the top_logprobs alternatives (the gateway forces those
// upstream for validation; the client never asked for them). OpenAI's shape is a
// present-but-empty array, matching the streaming-rewrite path.
func TestFilterClientInternalFields_EmptiesTopLogprobsWhenNotRequested(t *testing.T) {
	payload := []byte(`{"choices":[{"message":{"content":"Hi"},"logprobs":{"content":[{"token":"Hi","logprob":-0.1,"top_logprobs":[{"token":"Hi","logprob":-0.1}]}]}}]}`)

	filtered := filterClientInternalFields(payload, logprobClientIntent{keepLogprobs: true, keepTopLogprobs: false})

	require.JSONEq(t, `{"choices":[{"message":{"content":"Hi"},"logprobs":{"content":[{"token":"Hi","logprob":-0.1,"top_logprobs":[]}]}}]}`, string(filtered))
}

// Route A: zero intent (no client request) strips logprobs and top_logprobs --
// the historical default behavior.
func TestFilterClientInternalFields_StripsLogprobsByDefault(t *testing.T) {
	payload := []byte(`{"choices":[{"message":{"content":"Hi"},"logprobs":{"content":[{"token":"Hi","logprob":-0.1,"top_logprobs":[{"token":"Hi","logprob":-0.1}]}]}}]}`)

	filtered := filterClientInternalFields(payload, logprobClientIntent{})

	require.JSONEq(t, `{"choices":[{"message":{"content":"Hi"}}]}`, string(filtered))
}

// Route B (SSE-wrapped chat.completion rewrite): when the client asked for
// logprobs, the synthesized content chunk carries a reconstructed logprobs
// object with the token, logprob and top_logprobs.
func TestRewriteStreamingPayload_ReconstructsLogprobsInSynthesizedChunks(t *testing.T) {
	payload := []byte(`data: {"id":"cmpl-1","object":"chat.completion","created":123,"model":"Qwen","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"logprobs":{"content":[{"token":"Hi","logprob":-0.5,"bytes":[72,105],"top_logprobs":[{"token":"Hi","logprob":-0.5},{"token":"Hey","logprob":-1.5}]}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1}}` + "\n\n")

	rewritten := rewriteStreamingPayload(payload, logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})
	events := parseSSEChunks(t, string(rewritten))

	// role chunk, content chunk, usage chunk.
	require.Len(t, events, 3)
	contentChoice := events[1]["choices"].([]any)[0].(map[string]any)
	require.Equal(t, "Hi", contentChoice["delta"].(map[string]any)["content"])
	lp := contentChoice["logprobs"].(map[string]any)
	content := lp["content"].([]any)
	require.Len(t, content, 1)
	entry := content[0].(map[string]any)
	require.Equal(t, "Hi", entry["token"])
	require.Equal(t, -0.5, entry["logprob"])
	require.Len(t, entry["top_logprobs"].([]any), 2)
}

// Route B: logprobs requested but not top_logprobs -- the reconstructed chunk
// carries the per-token logprob with an empty top_logprobs array.
func TestRewriteStreamingPayload_ReconstructsLogprobsWithoutTopWhenNotRequested(t *testing.T) {
	payload := []byte(`data: {"id":"cmpl-1","object":"chat.completion","created":123,"model":"Qwen","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"logprobs":{"content":[{"token":"Hi","logprob":-0.5,"bytes":[72,105],"top_logprobs":[{"token":"Hi","logprob":-0.5},{"token":"Hey","logprob":-1.5}]}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1}}` + "\n\n")

	rewritten := rewriteStreamingPayload(payload, logprobClientIntent{keepLogprobs: true, keepTopLogprobs: false})
	events := parseSSEChunks(t, string(rewritten))

	entry := events[1]["choices"].([]any)[0].(map[string]any)["logprobs"].(map[string]any)["content"].([]any)[0].(map[string]any)
	require.Equal(t, "Hi", entry["token"])
	require.Empty(t, entry["top_logprobs"].([]any))
}

// Route A (existing chat.completion.chunk events, not synthesized): the !ok
// branch of rewriteStreamingPayload routes through filterClientInternalFields
// with the same intent, so a client who asked for logprobs keeps them on
// already-chunked SSE events too.
func TestRewriteStreamingPayload_KeepsLogprobsInExistingChunksWhenRequested(t *testing.T) {
	payload := []byte(`data: {"id":"cmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hi"},"logprobs":{"content":[{"token":"Hi","logprob":-0.1,"top_logprobs":[{"token":"Hi","logprob":-0.1}]}]},"finish_reason":null}]}` + "\n\n")

	rewritten := rewriteStreamingPayload(payload, logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})
	events := parseSSEChunks(t, string(rewritten))

	require.Len(t, events, 1)
	choice := events[0]["choices"].([]any)[0].(map[string]any)
	entry := choice["logprobs"].(map[string]any)["content"].([]any)[0].(map[string]any)
	require.Equal(t, "Hi", entry["token"])
	require.Len(t, entry["top_logprobs"].([]any), 1)
}

// Even when the client asked for logprobs, the always-internal fields stay
// stripped on the synthesized-chunk path. (Structurally guaranteed because
// chunks are rebuilt from typed structs, but asserted to lock the invariant.)
func TestRewriteStreamingPayload_StripsInternalFieldsEvenWhenLogprobsRequested(t *testing.T) {
	payload := []byte(`data: {"id":"cmpl-1","object":"chat.completion","created":123,"model":"Qwen","prompt_token_ids":[1,2],"prompt_logprobs":[{"x":1}],"choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"token_ids":[3,4],"logprobs":{"content":[{"token":"Hi","logprob":-0.5,"bytes":[72,105],"top_logprobs":[{"token":"Hi","logprob":-0.5}]}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1}}` + "\n\n")

	rewritten := rewriteStreamingPayload(payload, logprobClientIntent{keepLogprobs: true, keepTopLogprobs: true})

	require.NotContains(t, string(rewritten), "token_ids")
	require.NotContains(t, string(rewritten), "prompt_logprobs")
	require.Contains(t, string(rewritten), `"logprob":-0.5`)
}

// Route B: with no client logprobs request, the synthesized chunks carry no
// logprobs field at all (historical behavior preserved).
func TestRewriteStreamingPayload_OmitsLogprobsInSynthesizedChunksByDefault(t *testing.T) {
	payload := []byte(`data: {"id":"cmpl-1","object":"chat.completion","created":123,"model":"Qwen","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"logprobs":{"content":[{"token":"Hi","logprob":-0.5,"top_logprobs":[{"token":"Hi","logprob":-0.5}]}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1}}` + "\n\n")

	rewritten := rewriteStreamingPayload(payload, logprobClientIntent{})

	require.NotContains(t, string(rewritten), "logprob")
}

// The request filter forces logprobs/top_logprobs upstream for validation, but
// the captured chatRequest must still reflect the client's ORIGINAL intent so
// the response strip can honor it.
func TestNormalizeChatRequest_CapturesOriginalLogprobIntent(t *testing.T) {
	cases := []struct {
		name            string
		body            string
		wantLogprobs    bool
		wantTopLogprobs uint64
	}{
		{
			name:            "client asked logprobs and top_logprobs",
			body:            `{"model":"m","messages":[{"role":"user","content":"hi"}],"logprobs":true,"top_logprobs":3}`,
			wantLogprobs:    true,
			wantTopLogprobs: 3,
		},
		{
			name:            "client asked logprobs only",
			body:            `{"model":"m","messages":[{"role":"user","content":"hi"}],"logprobs":true}`,
			wantLogprobs:    true,
			wantTopLogprobs: 0,
		},
		{
			name:            "client asked logprobs with explicit top_logprobs 0",
			body:            `{"model":"m","messages":[{"role":"user","content":"hi"}],"logprobs":true,"top_logprobs":0}`,
			wantLogprobs:    true,
			wantTopLogprobs: 0,
		},
		{
			name:            "client asked neither",
			body:            `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			wantLogprobs:    false,
			wantTopLogprobs: 0,
		},
		{
			name:            "client explicitly disabled logprobs",
			body:            `{"model":"m","messages":[{"role":"user","content":"hi"}],"logprobs":false}`,
			wantLogprobs:    false,
			wantTopLogprobs: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			updated, req, err := normalizeChatRequest([]byte(tc.body))
			require.NoError(t, err)

			// The upstream body is still force-enabled for validation...
			require.Contains(t, string(updated), `"logprobs":true`)
			// ...but the captured request preserves the client's original intent.
			require.Equal(t, tc.wantLogprobs, req.Logprobs)
			require.Equal(t, tc.wantTopLogprobs, req.TopLogprobs)

			intent := logprobClientIntentFromRequest(req)
			require.Equal(t, tc.wantLogprobs, intent.keepLogprobs)
			require.Equal(t, tc.wantLogprobs && tc.wantTopLogprobs > 0, intent.keepTopLogprobs)
		})
	}
}
