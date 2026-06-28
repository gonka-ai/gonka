package paramvalidators

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Pins jsonMarshaledSize byte-for-byte against json.Marshal so a future
// encoding/json change can't drift CheckSize at the boundary.
func TestJSONMarshaledSizeMatchesJSONMarshal(t *testing.T) {
	cases := []struct {
		name string
		v    any
	}{
		{"empty object", map[string]any{}},
		{"nested object", map[string]any{
			"type":       "object",
			"properties": map[string]any{"city": map[string]any{"type": "string"}},
			"required":   []any{"city"},
		}},
		{"array of mixed values", map[string]any{"enum": []any{"a", json.Number("1"), nil, true, false}}},
		{"strings with html-unsafe chars", map[string]any{"description": `<script>"&"</script>`}},
		{"strings with control chars", map[string]any{"k": "tab\there\nnewlinectrl"}},
		{"string with U+2028 line separator", map[string]any{"k": "before after"}},
		{"string with U+2029 paragraph separator", map[string]any{"k": "before after"}},
		{"string with backslash and quote", map[string]any{"k": `quoted "value" and \backslash`}},
		{"json.Number values", map[string]any{"n": json.Number("123"), "f": json.Number("1.5e2"), "neg": json.Number("-42")}},
		{"large schema", map[string]any{
			"type":       "object",
			"properties": map[string]any{strings.Repeat("a", 100): map[string]any{"type": "string"}},
		}},
		{"nil value", map[string]any{"absent": nil}},
		{"deeply nested", map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": "x"}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expected, err := json.Marshal(tc.v)
			require.NoError(t, err)
			got, err := jsonMarshaledSize(tc.v)
			require.NoError(t, err)
			require.Equal(t, len(expected), got, "jsonMarshaledSize must equal len(json.Marshal(v))")
		})
	}
}

func TestJSONMarshaledSizePropagatesEncoderError(t *testing.T) {
	// Must error, not return 0 — otherwise CheckSize would silently accept.
	bad := map[string]any{"chan": make(chan int)}
	_, err := jsonMarshaledSize(bad)
	require.Error(t, err)
}
