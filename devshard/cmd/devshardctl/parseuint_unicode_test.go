package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A byte-expanding character (U+023A lowercases to U+2C65, 2 bytes -> 3) before
// the marker makes the index computed in strings.ToLower(msg) exceed len(msg);
// slicing the original msg with it panicked with slice-out-of-range.
func TestParseUintAfterMarkerByteExpandingUnicode(t *testing.T) {
	require.EqualValues(t, 0, parseContextLengthLimit("Ⱥmaximum context length is "))
	require.EqualValues(t, 0, parseContextLengthLimit("ȺȺmaximum context length is X"))
	require.EqualValues(t, 0, parseContextTotalRequested("Ⱥfor a total of at least "))
	require.EqualValues(t, 4096, parseContextLengthLimit("Ⱥmaximum context length is 4096 tokens"))
	require.EqualValues(t, 131072, parseContextLengthLimit("This model's maximum context length is 131072 tokens."))
	require.EqualValues(t, 4096, parseContextLengthLimit("maximum context length is 4096"), "a limit that ends the message must still parse")
	require.EqualValues(t, 4096, parseContextLengthLimit("Ⱥmaximum context length is 4096"))
}
