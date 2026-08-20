package heightsync_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
)

func TestFloorIndex_AsOfExcludesTheQueriedNonce(t *testing.T) {
	f := heightsync.NewFloorIndex()
	hash := []byte{0xaa}
	f.Observe(5, 100, hash)

	h, _, known := f.AsOf(5)
	require.True(t, known)
	require.Zero(t, h, "F(m) is the floor *below* m, so a stamp is never judged against itself")

	h, gotHash, known := f.AsOf(6)
	require.True(t, known)
	require.Equal(t, uint64(100), h)
	require.Equal(t, hash, gotHash, "the floor carries the hash of the block that set it")
}

func TestFloorIndex_RunningMaxIsMonotoneInNonce(t *testing.T) {
	f := heightsync.NewFloorIndex()
	hash := []byte{0xaa}
	f.Observe(1, 10, hash)
	f.Observe(2, 30, hash)
	f.Observe(3, 20, hash) // below the running max: no effect
	f.Observe(4, 40, hash)

	for _, tc := range []struct{ query, want uint64 }{
		{1, 0}, {2, 10}, {3, 30}, {4, 30}, {5, 40}, {99, 40},
	} {
		h, _, known := f.AsOf(tc.query)
		require.True(t, known)
		require.Equalf(t, tc.want, h, "F(%d)", tc.query)
	}
	require.Equal(t, 3, f.Len(), "a height below the running max adds no entry")
}

func TestFloorIndex_IgnoresAbsentStamps(t *testing.T) {
	f := heightsync.NewFloorIndex()
	f.Observe(1, 50, nil) // height without a hash is not a claim (H38)
	f.Observe(2, 0, []byte{0xaa})

	h, _, known := f.AsOf(10)
	require.True(t, known)
	require.Zero(t, h)
	require.Zero(t, f.Len())
}

func TestFloorIndex_PrunedRangeIsUnknownNotHigher(t *testing.T) {
	// The window is bounded, so an old enough nonce falls off the front. The
	// answer there must be "unknown" so callers skip the check: substituting the
	// oldest retained floor would reject honest stamps that predate it.
	f := heightsync.NewFloorIndex()
	hash := []byte{0xaa}
	for i := uint64(1); i <= heightsync.DefaultFloorWindow+10; i++ {
		f.Observe(i, i*10, hash)
	}
	require.Equal(t, heightsync.DefaultFloorWindow, f.Len(), "the index stays bounded")

	_, _, known := f.AsOf(2)
	require.False(t, known, "a pruned range is unknowable, not a higher floor")

	h, _, known := f.AsOf(heightsync.DefaultFloorWindow + 11)
	require.True(t, known)
	require.Equal(t, uint64((heightsync.DefaultFloorWindow+10)*10), h)
}

func TestFloorIndex_CloneIsIndependent(t *testing.T) {
	f := heightsync.NewFloorIndex()
	hash := []byte{0xaa}
	f.Observe(1, 10, hash)

	cp := f.Clone()
	cp.Observe(2, 99, hash)

	h, _, _ := f.AsOf(3)
	require.Equal(t, uint64(10), h, "trial-apply must not leak into committed state")
	h, _, _ = cp.AsOf(3)
	require.Equal(t, uint64(99), h)
}

// TestRefStamp_CoversEveryDiffResidentHeight pins the single-semantics rule: the
// log has one kind of height, so every message that can carry one is a reference
// stamp. Heartbeats and acks were once excluded as first-party own-tip claims;
// those readings now live in the envelope instead (spec §14).
func TestRefStamp_CoversEveryDiffResidentHeight(t *testing.T) {
	hash := []byte{0xaa}

	h, gotHash, ok := heightsync.RefStamp(hbTx(1, 50, 3, hash, nil))
	require.True(t, ok, "a heartbeat height is a reference height")
	require.Equal(t, uint64(50), h)
	require.Equal(t, hash, gotHash)

	h, _, ok = heightsync.RefStamp(ackTxAt(1, 4, 50, hash))
	require.True(t, ok, "an ack height is a reference height")
	require.Equal(t, uint64(50), h)

	h, gotHash, ok = heightsync.RefStamp(confirmTxAt(7, 50, hash))
	require.True(t, ok)
	require.Equal(t, uint64(50), h)
	require.Equal(t, hash, gotHash)

	// Absence is keyed on the hash, never on a zero height (H38).
	_, _, ok = heightsync.RefStamp(ackTxAt(1, 4, 50, nil))
	require.False(t, ok)
}

func TestRefProducingNonce_PerMessageBasis(t *testing.T) {
	hash := []byte{0xaa}

	// Sequencer-composed legs are produced at the nonce they land at.
	m, ok := heightsync.RefProducingNonce(9, startTxAt(9, 50, hash))
	require.True(t, ok)
	require.Equal(t, uint64(9), m)

	m, ok = heightsync.RefProducingNonce(9, hbTx(9, 50, 3, hash, nil))
	require.True(t, ok)
	require.Equal(t, uint64(9), m)

	// A confirm or finish is produced when its inference was prepared, which is
	// what inference_id records, so the basis needs no extra wire field.
	m, ok = heightsync.RefProducingNonce(9, confirmTxAt(3, 50, hash))
	require.True(t, ok)
	require.Equal(t, uint64(3), m)

	m, ok = heightsync.RefProducingNonce(9, finishTxAt(3, 50, hash))
	require.True(t, ok)
	require.Equal(t, uint64(3), m)

	// An ack's producer had applied through ref_nonce inclusive — it is answering
	// the heartbeat there — so the basis is one past it, folding in that
	// heartbeat's own stamp. Landing later, after the floor has moved, costs an
	// honest host nothing.
	m, ok = heightsync.RefProducingNonce(9, ackTxAt(3, 4, 50, hash))
	require.True(t, ok)
	require.Equal(t, uint64(4), m)
}
