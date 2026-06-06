package completionapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A single SSE chunk can carry multiple tokens in logprobs.content (speculative
// decoding, buffered flush at a reasoning boundary, chunked prefill). The
// enforced-token extraction must capture ALL of them; dropping the extras
// shortens the enforced sequence, which desyncs the validator replay and
// surfaces as similarity_below + inflated_tokens in production.

// makeStreamLines wraps raw chunk JSON bodies into SSE "data: " lines + [DONE].
func enforcedTokenStrings(et EnforcedTokens) []string {
	out := make([]string, 0, len(et.Tokens))
	for _, t := range et.Tokens {
		out = append(out, t.Token)
	}
	return out
}

func TestGetEnforcedTokens_MultiContentChunk_TokenIdsPath(t *testing.T) {
	// One chunk carries TWO tokens both in token_ids and logprobs.content.
	lines := []string{
		`data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":" over the"},"token_ids":[1312,276],"logprobs":{"content":[{"token":"1312","logprob":0.0,"bytes":[],"top_logprobs":[{"token":"1312","logprob":0.0}]},{"token":"276","logprob":0.0,"bytes":[],"top_logprobs":[{"token":"276","logprob":0.0}]}]},"finish_reason":null}]}`,
		`data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":" lazy"},"token_ids":[29292],"logprobs":{"content":[{"token":"29292","logprob":0.0,"bytes":[],"top_logprobs":[{"token":"29292","logprob":0.0}]}]},"finish_reason":null}]}`,
		`data: [DONE]`,
	}
	resp, err := NewCompletionResponseFromLines(lines)
	require.NoError(t, err)

	et, err := resp.GetEnforcedTokens()
	require.NoError(t, err)
	// Must capture all three tokens — NOT drop 276 from the multi-content chunk.
	require.Equal(t, []string{"1312", "276", "29292"}, enforcedTokenStrings(et),
		"multi-content chunk must contribute every token to enforced sequence")
}

func TestGetEnforcedTokens_MultiContentChunk_FallbackPath(t *testing.T) {
	// Same multi-content chunk but WITHOUT token_ids (pre-PR-29074 stream).
	// The fallback must still iterate every content entry.
	lines := []string{
		`data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":" over the"},"logprobs":{"content":[{"token":"1312","logprob":0.0,"bytes":[],"top_logprobs":[{"token":"1312","logprob":0.0}]},{"token":"276","logprob":0.0,"bytes":[],"top_logprobs":[{"token":"276","logprob":0.0}]}]},"finish_reason":null}]}`,
		`data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":" lazy"},"logprobs":{"content":[{"token":"29292","logprob":0.0,"bytes":[],"top_logprobs":[{"token":"29292","logprob":0.0}]}]},"finish_reason":null}]}`,
		`data: [DONE]`,
	}
	resp, err := NewCompletionResponseFromLines(lines)
	require.NoError(t, err)

	et, err := resp.GetEnforcedTokens()
	require.NoError(t, err)
	require.Equal(t, []string{"1312", "276", "29292"}, enforcedTokenStrings(et),
		"fallback path must not drop extra content entries in a multi-content chunk")
}

func TestGetEnforcedTokens_EnforcedLengthMatchesExtractLogits(t *testing.T) {
	// The enforced-token count must equal the ExtractLogits position count.
	// Any divergence reproduces the production desync (validator replays a
	// shorter sequence than the executor generated).
	lines := []string{
		`data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":" over the"},"token_ids":[1312,276],"logprobs":{"content":[{"token":"1312","logprob":0.0,"bytes":[],"top_logprobs":[{"token":"1312","logprob":0.0}]},{"token":"276","logprob":0.0,"bytes":[],"top_logprobs":[{"token":"276","logprob":0.0}]}]},"finish_reason":null}]}`,
		`data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":" lazy dog"},"token_ids":[29292,7751],"logprobs":{"content":[{"token":"29292","logprob":0.0,"bytes":[],"top_logprobs":[{"token":"29292","logprob":0.0}]},{"token":"7751","logprob":0.0,"bytes":[],"top_logprobs":[{"token":"7751","logprob":0.0}]}]},"finish_reason":null}]}`,
		`data: [DONE]`,
	}
	resp, err := NewCompletionResponseFromLines(lines)
	require.NoError(t, err)

	et, err := resp.GetEnforcedTokens()
	require.NoError(t, err)
	logits := resp.ExtractLogits()
	require.Equal(t, len(logits), len(et.Tokens),
		"enforced token count must equal ExtractLogits position count")
}
