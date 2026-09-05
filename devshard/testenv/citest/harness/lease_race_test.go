package harness

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaxDiffNonceJSON(t *testing.T) {
	n, err := maxDiffNonceJSON(`[{"diff":{"nonce":2},"state_hash":"YQ=="},{"diff":{"nonce":5},"state_hash":"Yg=="}]`)
	require.NoError(t, err)
	require.Equal(t, uint64(5), n)

	n, err = maxDiffNonceJSON("[]")
	require.NoError(t, err)
	require.Equal(t, uint64(0), n)

	n, err = maxDiffNonceJSON("")
	require.NoError(t, err)
	require.Equal(t, uint64(0), n)

	_, err = maxDiffNonceJSON("{")
	require.Error(t, err)
}
