package transport

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
)

func TestDecodeMainnetBlockHashHex_OversizedRejected(t *testing.T) {
	huge := strings.Repeat("aa", heightsync.MaxObservedBlockHashBytes+1)
	_, err := decodeMainnetBlockHashHex(huge)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds")

	ok := strings.Repeat("ab", heightsync.MaxObservedBlockHashBytes)
	got, err := decodeMainnetBlockHashHex(ok)
	require.NoError(t, err)
	require.Len(t, got, heightsync.MaxObservedBlockHashBytes)
}
