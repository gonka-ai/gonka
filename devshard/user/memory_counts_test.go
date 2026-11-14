package user

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestSessionMemoryCountsReadsLengths(t *testing.T) {
	s := &Session{
		escrowID: "escrow-1",
		diffs:    []types.Diff{{Nonce: 1}, {Nonce: 2}, {Nonce: 3}},
		pendingTxs: []*types.DevshardTx{
			{},
			{},
		},
		signatures: map[uint64]map[uint32][]byte{
			1: {0: {1}},
			2: {0: {2}},
		},
		nonceStates:   map[uint64]*nonceOutcome{9: {}},
		appliedTxKeys: map[string]struct{}{"a": {}, "b": {}},
		pendingTxKeys: map[string]struct{}{"c": {}},
		catchingUp:    make([]atomic.Int32, 3),
	}
	s.catchingUp[1].Store(2)
	s.catchingUp[2].Store(1)

	got := s.MemoryCounts()
	require.Equal(t, "escrow-1", got.EscrowID)
	require.Equal(t, 3, got.RetainedDiffs)
	require.Equal(t, 2, got.PendingTxs)
	require.Equal(t, 2, got.SignatureNonces)
	require.Equal(t, 1, got.NonceStates)
	require.Equal(t, 2, got.AppliedTxKeys)
	require.Equal(t, 1, got.PendingTxKeys)
	require.Equal(t, 2, got.HostsCatchingUp)
	require.Zero(t, got.Live)
	require.Zero(t, got.Sealed)

	require.Zero(t, (*Session)(nil).MemoryCounts())
}
