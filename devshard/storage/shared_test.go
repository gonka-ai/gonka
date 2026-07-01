package storage

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

const storageTestVersion = "v1"

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
		EpochID:        7,
		Version:        storageTestVersion,
		CreatorAddr:    "creator",
		Config:         types.SessionConfig{},
		Group:          defaultGroup(),
		InitialBalance: 1000,
	}
}

func paramsForEpoch(escrowID string, epochID uint64) CreateSessionParams {
	p := defaultParams()
	p.EscrowID = escrowID
	p.EpochID = epochID
	return p
}

func runCreateSession_GetSessionMeta(t *testing.T, store Storage) {
	t.Helper()

	err := store.CreateSession(defaultParams())
	require.NoError(t, err)

	meta, err := store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, "escrow-1", meta.EscrowID)
	require.Equal(t, uint64(7), meta.EpochID)
	require.Equal(t, storageTestVersion, meta.Version)
	require.Equal(t, types.DefaultInferenceSealGraceNonces(len(meta.Group)), meta.Config.InferenceSealGraceNonces)
	require.Equal(t, uint32(types.DefaultInferenceSealGraceSeconds), meta.Config.InferenceSealGraceSeconds)
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
	require.Equal(t, uint64(7), meta.EpochID)
	require.Equal(t, storageTestVersion, meta.Version)
	require.Equal(t, uint64(1000), meta.InitialBalance)
}

func runCreateSession_ConflictingEpoch(t *testing.T, store Storage) {
	t.Helper()

	require.NoError(t, store.CreateSession(defaultParams()))
	err := store.CreateSession(paramsForEpoch("escrow-1", 8))
	require.ErrorIs(t, err, ErrSessionEpochConflict)

	meta, metaErr := store.GetSessionMeta("escrow-1")
	require.NoError(t, metaErr)
	require.Equal(t, uint64(7), meta.EpochID)
}

func runCreateSession_ConflictingVersion(t *testing.T, store Storage) {
	t.Helper()

	require.NoError(t, store.CreateSession(defaultParams()))
	p := defaultParams()
	p.Version = "other-runtime"
	err := store.CreateSession(p)
	require.ErrorIs(t, err, ErrSessionVersionConflict)

	meta, metaErr := store.GetSessionMeta("escrow-1")
	require.NoError(t, metaErr)
	require.Equal(t, storageTestVersion, meta.Version)
}

// runCreateSession_EmptyVersionRejected pins the storage-boundary contract:
// CreateSession must reject an empty Version tag.
func runCreateSession_EmptyVersionRejected(t *testing.T, store Storage) {
	t.Helper()

	p := defaultParams()
	p.Version = ""
	err := store.CreateSession(p)
	require.ErrorIs(t, err, ErrSessionVersionRequired)
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

func runSaveLoadSnapshot(t *testing.T, store Storage) {
	t.Helper()

	require.NoError(t, store.CreateSession(defaultParams()))

	require.ErrorIs(t, func() error {
		_, _, err := store.LoadSnapshot("escrow-1")
		return err
	}(), ErrSnapshotNotFound)

	require.NoError(t, store.SaveSnapshot("escrow-1", 500, []byte("state-500")))
	nonce, data, err := store.LoadSnapshot("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(500), nonce)
	require.Equal(t, []byte("state-500"), data)

	data[0] = 'X'
	_, data, err = store.LoadSnapshot("escrow-1")
	require.NoError(t, err)
	require.Equal(t, []byte("state-500"), data)

	require.NoError(t, store.SaveSnapshot("escrow-1", 1000, []byte("state-1000")))
	nonce, data, err = store.LoadSnapshot("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(1000), nonce)
	require.Equal(t, []byte("state-1000"), data)

	require.NoError(t, store.SaveSnapshot("escrow-1", 750, []byte("state-750")))
	nonce, data, err = store.LoadSnapshot("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(1000), nonce)
	require.Equal(t, []byte("state-1000"), data, "older async snapshots must not overwrite newer snapshots")
}

func testInferenceRow(id uint64) InferenceRow {
	return InferenceRow{
		InferenceID: id,
		SealedNonce: 42,
	}
}

func runSealedInferenceLifecycle(t *testing.T, store Storage) {
	t.Helper()

	require.NoError(t, store.CreateSession(defaultParams()))

	row := testInferenceRow(1)
	require.NoError(t, store.InsertSealedInference("escrow-1", row))

	got, ok, err := store.GetSealedInference("escrow-1", 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, row, got)

	// Upsert updates the same inference id (e.g. challenged before seal, then sealed).
	row.SealedStatus = 5
	require.NoError(t, store.InsertSealedInference("escrow-1", row))
	got, ok, err = store.GetSealedInference("escrow-1", 1)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint32(5), got.SealedStatus)

	require.NoError(t, store.DeleteSealedInferences("escrow-1"))
	_, ok, err = store.GetSealedInference("escrow-1", 1)
	require.NoError(t, err)
	require.False(t, ok)
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

	require.NoError(t, store.CreateSession(paramsForEpoch("escrow-1", 7)))
	require.NoError(t, store.CreateSession(paramsForEpoch("escrow-2", 7)))
	require.NoError(t, store.CreateSession(paramsForEpoch("escrow-3", 8)))

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Len(t, active, 3)

	require.NoError(t, store.MarkSettled("escrow-2"))

	active, err = store.ListActiveSessions()
	require.NoError(t, err)
	require.Len(t, active, 2)

	for _, a := range active {
		require.NotEqual(t, "escrow-2", a.EscrowID)
		// Each ActiveSession must carry its epoch back.
		switch a.EscrowID {
		case "escrow-1":
			require.Equal(t, uint64(7), a.EpochID)
		case "escrow-3":
			require.Equal(t, uint64(8), a.EpochID)
		}
	}
}

// runPruneEpoch_RemovesOnlyTarget verifies the core promise of partitioned
// storage: prune deletes every session in the target epoch and leaves all
// other epochs byte-for-byte intact.
func runPruneEpoch_RemovesOnlyTarget(t *testing.T, store Storage) {
	t.Helper()

	// Two sessions in epoch 7, one in epoch 8, one in epoch 9.
	require.NoError(t, store.CreateSession(paramsForEpoch("e7a", 7)))
	require.NoError(t, store.CreateSession(paramsForEpoch("e7b", 7)))
	require.NoError(t, store.CreateSession(paramsForEpoch("e8", 8)))
	require.NoError(t, store.CreateSession(paramsForEpoch("e9", 9)))

	// Add diffs + signatures so we can verify they survive.
	for _, esc := range []string{"e7a", "e7b", "e8", "e9"} {
		require.NoError(t, store.AppendDiff(esc, types.DiffRecord{
			Diff:       types.Diff{Nonce: 1, UserSig: []byte("sig")},
			StateHash:  []byte{0x01},
			Signatures: map[uint32][]byte{0: {0x01}},
		}))
		require.NoError(t, store.AddSignature(esc, 1, 1, []byte("ext-sig")))
		require.NoError(t, store.MarkFinalized(esc, 1))
	}

	// Drop epoch 7.
	require.NoError(t, store.PruneEpoch(7))

	// Both epoch-7 sessions must be gone from meta and from active list.
	for _, esc := range []string{"e7a", "e7b"} {
		_, err := store.GetSessionMeta(esc)
		require.Error(t, err, "session %s should be gone", esc)
	}

	// Epoch 8 and 9 are intact: meta, diffs, signatures, last_finalized.
	for _, esc := range []string{"e8", "e9"} {
		meta, err := store.GetSessionMeta(esc)
		require.NoError(t, err, "session %s should survive prune", esc)
		require.Equal(t, "active", meta.Status)
		require.Equal(t, uint64(1), meta.LatestNonce)

		diffs, err := store.GetDiffs(esc, 1, 1)
		require.NoError(t, err)
		require.Len(t, diffs, 1)
		require.Len(t, diffs[0].Signatures, 2, "both sigs preserved on %s", esc)

		last, err := store.LastFinalized(esc)
		require.NoError(t, err)
		require.Equal(t, uint64(1), last)
	}

	// Active list should now show two entries: e8 and e9.
	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Len(t, active, 2)
	ids := make([]string, 0, len(active))
	for _, a := range active {
		ids = append(ids, a.EscrowID)
	}
	sort.Strings(ids)
	require.Equal(t, []string{"e8", "e9"}, ids)
}

// runPruneEpoch_Idempotent: pruning the same epoch twice, or pruning an epoch
// with no sessions, must not error.
func runPruneEpoch_Idempotent(t *testing.T, store Storage) {
	t.Helper()

	require.NoError(t, store.CreateSession(paramsForEpoch("e7", 7)))

	// Prune unknown epoch — no-op.
	require.NoError(t, store.PruneEpoch(99))

	// Prune real epoch.
	require.NoError(t, store.PruneEpoch(7))

	// Second prune of the same epoch — no-op.
	require.NoError(t, store.PruneEpoch(7))

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Empty(t, active)
}

// runPruneEpoch_WriteAfter: after pruning an epoch, new sessions in that same
// epoch number must be writable again. Verifies the partition was fully torn
// down (no leftover unique-key collisions or partition state).
func runPruneEpoch_WriteAfter(t *testing.T, store Storage) {
	t.Helper()

	require.NoError(t, store.CreateSession(paramsForEpoch("alpha", 7)))
	require.NoError(t, store.AppendDiff("alpha", makeDiffRecord(1)))

	require.NoError(t, store.PruneEpoch(7))

	// Recreate a session with the SAME escrow id in the SAME epoch number
	// after the prune. Must succeed and behave as a fresh session.
	require.NoError(t, store.CreateSession(paramsForEpoch("alpha", 7)))
	meta, err := store.GetSessionMeta("alpha")
	require.NoError(t, err)
	require.Equal(t, uint64(0), meta.LatestNonce, "latest_nonce must reset after prune")

	require.NoError(t, store.AppendDiff("alpha", makeDiffRecord(1)))
	diffs, err := store.GetDiffs("alpha", 1, 1)
	require.NoError(t, err)
	require.Len(t, diffs, 1)
}
