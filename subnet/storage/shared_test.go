package storage

import (
	"testing"

	"github.com/stretchr/testify/require"

	"subnet/types"
)

func makeDiffRecord(nonce uint64) types.DiffRecord {
	return types.DiffRecord{
		Diff: types.Diff{
			Nonce:   nonce,
			UserSig: []byte("sig"),
		},
		StateHash:  []byte{byte(nonce)},
		Signatures: map[uint32][]byte{0: {byte(nonce)}},
	}
}

func defaultGroup() []types.SlotAssignment {
	return []types.SlotAssignment{
		{SlotID: 0, ValidatorAddress: "addr0"},
		{SlotID: 1, ValidatorAddress: "addr1"},
	}
}

func defaultParams() CreateSessionParams {
	return CreateSessionParams{
		EscrowID:       "escrow-1",
		CreatorAddr:    "creator",
		Config:         types.SessionConfig{},
		Group:          defaultGroup(),
		InitialBalance: 1000,
	}
}

func runCreateSession_GetSessionMeta(t *testing.T, store Storage) {
	t.Helper()

	err := store.CreateSession(defaultParams())
	require.NoError(t, err)

	meta, err := store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, "escrow-1", meta.EscrowID)
	require.Equal(t, "creator", meta.CreatorAddr)
	require.Equal(t, uint64(1000), meta.InitialBalance)
	require.Len(t, meta.Group, 2)
	require.Equal(t, uint64(0), meta.LatestNonce)
	require.Equal(t, "active", meta.Status)
}

func runCreateSession_Idempotent(t *testing.T, store Storage) {
	t.Helper()

	err := store.CreateSession(defaultParams())
	require.NoError(t, err)

	// Second call with same params must not error.
	err = store.CreateSession(defaultParams())
	require.NoError(t, err)

	// Data from first call must be intact.
	meta, err := store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, "escrow-1", meta.EscrowID)
	require.Equal(t, uint64(1000), meta.InitialBalance)
}

func runAppendDiff_GetDiffs(t *testing.T, store Storage) {
	t.Helper()

	err := store.CreateSession(defaultParams())
	require.NoError(t, err)

	for i := uint64(1); i <= 5; i++ {
		err = store.AppendDiff("escrow-1", types.DiffRecord{
			Diff: types.Diff{
				Nonce:   i,
				UserSig: []byte("sig"),
			},
			StateHash:  []byte{byte(i)},
			Signatures: map[uint32][]byte{0: {byte(i)}},
		})
		require.NoError(t, err)
	}

	diffs, err := store.GetDiffs("escrow-1", 2, 4)
	require.NoError(t, err)
	require.Len(t, diffs, 3)
	require.Equal(t, uint64(2), diffs[0].Nonce)
	require.Equal(t, uint64(3), diffs[1].Nonce)
	require.Equal(t, uint64(4), diffs[2].Nonce)

	// Verify latest_nonce updated.
	meta, err := store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(5), meta.LatestNonce)
}

func runGetSignatures(t *testing.T, store Storage) {
	t.Helper()

	err := store.CreateSession(defaultParams())
	require.NoError(t, err)

	err = store.AppendDiff("escrow-1", types.DiffRecord{
		Diff:       types.Diff{Nonce: 1, UserSig: []byte("sig")},
		StateHash:  []byte{0x01},
		Signatures: map[uint32][]byte{},
	})
	require.NoError(t, err)

	err = store.AddSignature("escrow-1", 1, 0, []byte("sig-0"))
	require.NoError(t, err)
	err = store.AddSignature("escrow-1", 1, 2, []byte("sig-2"))
	require.NoError(t, err)

	sigs, err := store.GetSignatures("escrow-1", 1)
	require.NoError(t, err)
	require.Len(t, sigs, 2)
	require.Equal(t, []byte("sig-0"), sigs[0])
	require.Equal(t, []byte("sig-2"), sigs[2])

	// Mutating returned map should not affect storage.
	sigs[99] = []byte("bad")
	sigs2, err := store.GetSignatures("escrow-1", 1)
	require.NoError(t, err)
	require.Len(t, sigs2, 2)
}

func runMarkFinalized_LastFinalized(t *testing.T, store Storage) {
	t.Helper()

	err := store.CreateSession(defaultParams())
	require.NoError(t, err)

	// Initially zero.
	last, err := store.LastFinalized("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(0), last)

	// Mark nonce 3 finalized.
	err = store.MarkFinalized("escrow-1", 3)
	require.NoError(t, err)
	last, err = store.LastFinalized("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(3), last)

	// Mark nonce 5 finalized.
	err = store.MarkFinalized("escrow-1", 5)
	require.NoError(t, err)
	last, err = store.LastFinalized("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(5), last)

	// Idempotent: marking 3 again doesn't regress.
	err = store.MarkFinalized("escrow-1", 3)
	require.NoError(t, err)
	last, err = store.LastFinalized("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(5), last)
}

func runAddSignature(t *testing.T, store Storage) {
	t.Helper()

	err := store.CreateSession(defaultParams())
	require.NoError(t, err)

	err = store.AppendDiff("escrow-1", types.DiffRecord{
		Diff: types.Diff{
			Nonce:   1,
			UserSig: []byte("sig"),
		},
		StateHash:  []byte{0x01},
		Signatures: map[uint32][]byte{},
	})
	require.NoError(t, err)

	err = store.AddSignature("escrow-1", 1, 3, []byte("host-sig-3"))
	require.NoError(t, err)

	diffs, err := store.GetDiffs("escrow-1", 1, 1)
	require.NoError(t, err)
	require.Len(t, diffs, 1)
	require.Equal(t, []byte("host-sig-3"), diffs[0].Signatures[3])
}

func runWarmKeyDelta(t *testing.T, store Storage) {
	t.Helper()

	err := store.CreateSession(defaultParams())
	require.NoError(t, err)

	delta := map[uint32]string{0: "warm-addr-0", 1: "warm-addr-1"}
	err = store.AppendDiff("escrow-1", types.DiffRecord{
		Diff: types.Diff{
			Nonce:   1,
			UserSig: []byte("sig"),
		},
		StateHash:    []byte{0x01},
		WarmKeyDelta: delta,
	})
	require.NoError(t, err)

	// Append a diff without warm keys.
	err = store.AppendDiff("escrow-1", types.DiffRecord{
		Diff: types.Diff{
			Nonce:   2,
			UserSig: []byte("sig2"),
		},
		StateHash: []byte{0x02},
	})
	require.NoError(t, err)

	diffs, err := store.GetDiffs("escrow-1", 1, 2)
	require.NoError(t, err)
	require.Len(t, diffs, 2)

	require.Equal(t, delta, diffs[0].WarmKeyDelta)
	require.Nil(t, diffs[1].WarmKeyDelta)
}

func runMarkSettled(t *testing.T, store Storage) {
	t.Helper()

	err := store.CreateSession(defaultParams())
	require.NoError(t, err)

	meta, err := store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, "active", meta.Status)

	err = store.MarkSettled("escrow-1")
	require.NoError(t, err)

	meta, err = store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, "settled", meta.Status)
}

func runListActiveSessions(t *testing.T, store Storage) {
	t.Helper()

	p1 := defaultParams()
	p1.EscrowID = "escrow-1"
	p2 := defaultParams()
	p2.EscrowID = "escrow-2"
	p3 := defaultParams()
	p3.EscrowID = "escrow-3"

	require.NoError(t, store.CreateSession(p1))
	require.NoError(t, store.CreateSession(p2))
	require.NoError(t, store.CreateSession(p3))

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Len(t, active, 3)

	require.NoError(t, store.MarkSettled("escrow-2"))

	active, err = store.ListActiveSessions()
	require.NoError(t, err)
	require.Len(t, active, 2)

	// escrow-2 should not be in the list.
	for _, id := range active {
		require.NotEqual(t, "escrow-2", id)
	}
}
