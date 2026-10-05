package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// U+023A lowercases to U+2C65, which is one byte longer, so an index computed in
// strings.ToLower(msg) can exceed len(msg). The old code sliced the original msg
// with that index: it panicked when the marker ended the string and mis-parsed
// the number otherwise. parseUintAfterMarker must slice the lowercased copy.
func TestParseUintAfterMarkerByteExpandingUnicode(t *testing.T) {
	// Marker ends the string: must not panic, no digits -> 0.
	require.EqualValues(t, 0, parseContextLengthLimit("Ⱥmaximum context length is "))
	// Two expanders before the marker, a non-digit after (qw3rjo trigger shape): no panic -> 0.
	require.EqualValues(t, 0, parseContextLengthLimit("ȺȺmaximum context length is X"))
	// Expander before the marker, digits then a delimiter: the real number, not an off-by-expansion value (the old msg-slice returned 96 here).
	require.EqualValues(t, 4096, parseContextLengthLimit("Ⱥmaximum context length is 4096 tokens"))
	// Plain ASCII is unchanged.
	require.EqualValues(t, 131072, parseContextLengthLimit("This model's maximum context length is 131072 tokens."))
}
