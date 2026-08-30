package heightsync_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/types"
)

// TestFilterHostRaises_MismatchedEnvelopeOmitsRaise is the compose half of spec
// §14: a host stamp > F with envelope t < stamp is not composed as a raise.
// Gossip of whatever did land still yields the same F — Observe never sees the
// omitted tx.
func TestFilterHostRaises_MismatchedEnvelopeOmitsRaise(t *testing.T) {
	hash := []byte{0xaa}
	own := map[uint32]struct{}{0: {}}
	ack := ackTxAt(1, 0, 180, hash)

	kept, n := heightsync.FilterHostRaises(100, 90, true, own, []*types.DevshardTx{ack})
	require.Equal(t, 1, n)
	require.Empty(t, kept)

	// Matching envelope: the raise is this hop's first-party tip.
	kept, n = heightsync.FilterHostRaises(100, 180, true, own, []*types.DevshardTx{ack})
	require.Zero(t, n)
	require.Len(t, kept, 1)

	// Carry (stamp == F) with a lower envelope is the honest lagging path.
	carry := ackTxAt(1, 0, 100, hash)
	kept, n = heightsync.FilterHostRaises(100, 90, true, own, []*types.DevshardTx{carry})
	require.Zero(t, n)
	require.Len(t, kept, 1)

	// No envelope (in-process / omit): do not drop; L4 cannot run anyway.
	kept, n = heightsync.FilterHostRaises(100, 0, false, own, []*types.DevshardTx{ack})
	require.Zero(t, n)
	require.Len(t, kept, 1)

	// Another slot's gossiped ack is not bound to this hop's envelope.
	other := ackTxAt(1, 1, 180, hash)
	kept, n = heightsync.FilterHostRaises(100, 90, true, own, []*types.DevshardTx{other})
	require.Zero(t, n)
	require.Len(t, kept, 1)
}

func TestFilterHostRaises_SequencerStampsPass(t *testing.T) {
	hash := []byte{0xaa}
	hb := hbTx(1, 180, 3, hash, nil)
	kept, n := heightsync.FilterHostRaises(100, 90, true, map[uint32]struct{}{0: {}}, []*types.DevshardTx{hb})
	require.Zero(t, n)
	require.Len(t, kept, 1)
}
