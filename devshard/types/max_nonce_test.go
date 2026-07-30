package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFinalizeNonceReserve(t *testing.T) {
	require.Equal(t, uint64(17), FinalizeNonceReserve(16))
	require.Equal(t, uint64(4), FinalizeNonceReserve(3))
}

func TestMaxActiveNonce(t *testing.T) {
	require.Equal(t, ^uint64(0), MaxActiveNonce(0, 16))
	require.Equal(t, uint64(999_983), MaxActiveNonce(1_000_000, 16))
	require.Equal(t, uint64(0), MaxActiveNonce(10, 16))
}

func TestDiffHasActiveCompletionWork(t *testing.T) {
	require.False(t, DiffHasActiveCompletionWork(Diff{}))
	require.True(t, DiffHasActiveCompletionWork(Diff{
		Txs: []*DevshardTx{{Tx: &DevshardTx_StartInference{StartInference: &MsgStartInference{}}}},
	}))
}
