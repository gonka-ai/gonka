package completionapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStripTopLevelNullUsageFromSSELine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{
			name:    "usage null between keys",
			in:      `data: {"id":"x","usage":null,"choices":[{"index":0,"delta":{"content":"a"}}]}`,
			want:    `data: {"id":"x","choices":[{"index":0,"delta":{"content":"a"}}]}`,
			changed: true,
		},
		{
			name:    "usage null last key",
			in:      `data: {"id":"x","choices":[],"usage":null}`,
			want:    `data: {"id":"x","choices":[]}`,
			changed: true,
		},
		{
			name:    "usage null first key",
			in:      `data: {"usage":null,"id":"x","choices":[]}`,
			want:    `data: {"id":"x","choices":[]}`,
			changed: true,
		},
		{
			name: "usage null with spaces",
			// Surgical removal keeps surrounding whitespace; only the pair + comma go.
			in:      `data: {"id":"x", "usage": null, "choices":[]}`,
			want:    `data: {"id":"x",  "choices":[]}`,
			changed: true,
		},
		{
			name:    "real usage object preserved",
			in:      `data: {"id":"x","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
			want:    `data: {"id":"x","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
			changed: false,
		},
		{
			name:    "nested usage null untouched",
			in:      `data: {"id":"x","choices":[{"delta":{"usage":null}}],"model":"m"}`,
			want:    `data: {"id":"x","choices":[{"delta":{"usage":null}}],"model":"m"}`,
			changed: false,
		},
		{
			name:    "done marker",
			in:      `data: [DONE]`,
			want:    `data: [DONE]`,
			changed: false,
		},
		{
			name:    "non data line",
			in:      `: keep-alive`,
			want:    `: keep-alive`,
			changed: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, changed := stripTopLevelNullUsageFromSSELine([]byte(tc.in))
			require.Equal(t, tc.changed, changed)
			require.Equal(t, tc.want, string(got))
			if changed {
				require.True(t, json.Valid([]byte(got[len("data: "):])), "stripped body must remain valid JSON")
			}
		})
	}
}

func TestRemoveTopLevelUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{
			name:    "real usage object alongside choices",
			in:      `{"id":"x","choices":[{"index":0,"delta":{"content":"a"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
			want:    `{"id":"x","choices":[{"index":0,"delta":{"content":"a"},"finish_reason":"stop"}]}`,
			changed: true,
		},
		{
			name:    "usage object first key",
			in:      `{"usage":{"total_tokens":3},"id":"x","choices":[]}`,
			want:    `{"id":"x","choices":[]}`,
			changed: true,
		},
		{
			name:    "usage null removed too",
			in:      `{"id":"x","usage":null,"choices":[]}`,
			want:    `{"id":"x","choices":[]}`,
			changed: true,
		},
		{
			name:    "nested usage untouched",
			in:      `{"id":"x","choices":[{"delta":{"usage":{"a":1}}}]}`,
			want:    `{"id":"x","choices":[{"delta":{"usage":{"a":1}}}]}`,
			changed: false,
		},
		{
			name:    "no usage key",
			in:      `{"id":"x","choices":[]}`,
			want:    `{"id":"x","choices":[]}`,
			changed: false,
		},
		{
			name:    "not an object",
			in:      `[1,2,3]`,
			want:    `[1,2,3]`,
			changed: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, changed := RemoveTopLevelUsage([]byte(tc.in))
			require.Equal(t, tc.changed, changed)
			require.Equal(t, tc.want, string(got))
			require.True(t, json.Valid(got), "result must remain valid JSON")
		})
	}
}

func TestTopLevelJSONValue(t *testing.T) {
	t.Parallel()

	body := []byte(`{"id":"x","choices":[ ],"usage":{"total_tokens":3}}`)

	choices, ok := TopLevelJSONValue(body, "choices")
	require.True(t, ok)
	require.Equal(t, `[ ]`, string(choices))

	usage, ok := TopLevelJSONValue(body, "usage")
	require.True(t, ok)
	require.Equal(t, `{"total_tokens":3}`, string(usage))

	_, ok = TopLevelJSONValue(body, "model")
	require.False(t, ok)

	// A nested key never satisfies a depth-1 lookup.
	_, ok = TopLevelJSONValue([]byte(`{"choices":[{"usage":1}]}`), "usage")
	require.False(t, ok)
}

func TestProcessStreamedResponseStripsNullUsage(t *testing.T) {
	t.Parallel()

	processor := NewExecutorResponseProcessor("inf-1")
	in := `data: {"id":"upstream","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}],"usage":null}`
	got, err := processor.ProcessStreamedResponse(in)
	require.NoError(t, err)
	require.NotContains(t, got, `"usage"`)
	require.Contains(t, got, `"content":"Hi"`)
	require.Equal(t, "inf-1", semanticJSON(t, got)["id"])

	// Final include_usage chunk with a real object must survive.
	usageLine := `data: {"id":"upstream","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1}}`
	got, err = processor.ProcessStreamedResponse(usageLine)
	require.NoError(t, err)
	require.Contains(t, got, `"prompt_tokens":3`)
	require.Equal(t, "inf-1", semanticJSON(t, got)["id"])
}
