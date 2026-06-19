package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func newTestSQLite(t *testing.T) *SQLite {
	t.Helper()
	db, err := NewSQLite(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

// Conformance tests (shared with Memory).

func TestSQLite_CreateSession_GetSessionMeta(t *testing.T) {
	runCreateSession_GetSessionMeta(t, newTestSQLite(t))
}

func TestSQLite_CreateSession_Idempotent(t *testing.T) {
	runCreateSession_Idempotent(t, newTestSQLite(t))
}

func TestSQLite_CreateSession_ConcurrentIdempotent(t *testing.T) {
	db := newTestSQLite(t)
	params := defaultParams()

	const attempts = 20
	var wg sync.WaitGroup
	errs := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- db.CreateSession(params)
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	meta, err := db.GetSessionMeta(params.EscrowID)
	require.NoError(t, err)
	require.Equal(t, params.EpochID, meta.EpochID)
}

func TestSQLite_CreateSession_ConcurrentEpochConflict(t *testing.T) {
	db := newTestSQLite(t)

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, epochID := range []uint64{7, 8} {
		wg.Add(1)
		go func(epochID uint64) {
			defer wg.Done()
			<-start
			errs <- db.CreateSession(paramsForEpoch("same-escrow", epochID))
		}(epochID)
	}
	close(start)
	wg.Wait()
	close(errs)

	var success, conflict int
	for err := range errs {
		if err == nil {
			success++
			continue
		}
		require.ErrorIs(t, err, ErrSessionEpochConflict)
		conflict++
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, conflict)

	active, err := db.ListActiveSessions()
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, "same-escrow", active[0].EscrowID)
	_, ok, err := db.findSessionEpoch("same-escrow")
	require.NoError(t, err)
	require.True(t, ok)
}

func TestSQLite_CreateSession_ConflictingEpoch(t *testing.T) {
	runCreateSession_ConflictingEpoch(t, newTestSQLite(t))
}

func TestSQLite_CreateSession_ConflictingVersion(t *testing.T) {
	runCreateSession_ConflictingVersion(t, newTestSQLite(t))
}

func TestSQLite_CreateSession_EmptyVersionRejected(t *testing.T) {
	runCreateSession_EmptyVersionRejected(t, newTestSQLite(t))
}

func TestSQLite_AppendDiff_GetDiffs(t *testing.T) {
	runAppendDiff_GetDiffs(t, newTestSQLite(t))
}

func TestSQLite_GetSignatures(t *testing.T) {
	runGetSignatures(t, newTestSQLite(t))
}

func TestSQLite_MarkFinalized_LastFinalized(t *testing.T) {
	runMarkFinalized_LastFinalized(t, newTestSQLite(t))
}

func TestSQLite_SaveLoadSnapshot(t *testing.T) {
	runSaveLoadSnapshot(t, newTestSQLite(t))
}

func TestSQLite_SealedInferenceLifecycle(t *testing.T) {
	runSealedInferenceLifecycle(t, newTestSQLite(t))
}

func TestSQLite_AddSignature(t *testing.T) {
	runAddSignature(t, newTestSQLite(t))
}

func TestSQLite_WarmKeyDelta(t *testing.T) {
	runWarmKeyDelta(t, newTestSQLite(t))
}

func TestSQLite_MarkSettled(t *testing.T) {
	runMarkSettled(t, newTestSQLite(t))
}

func TestSQLite_ListActiveSessions(t *testing.T) {
	runListActiveSessions(t, newTestSQLite(t))
}

func TestSQLite_PruneEpoch_RemovesOnlyTarget(t *testing.T) {
	runPruneEpoch_RemovesOnlyTarget(t, newTestSQLite(t))
}

func TestSQLite_PruneEpoch_Idempotent(t *testing.T) {
	runPruneEpoch_Idempotent(t, newTestSQLite(t))
}

func TestSQLite_PruneEpoch_WriteAfter(t *testing.T) {
	runPruneEpoch_WriteAfter(t, newTestSQLite(t))
}

// SQLite-specific durability tests.

func TestSQLite_PersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()

	// Phase 1: write data.
	db1, err := NewSQLite(dir)
	require.NoError(t, err)

	require.NoError(t, db1.CreateSession(defaultParams()))

	for i := uint64(1); i <= 10; i++ {
		delta := map[uint32]string{uint32(i % 3): fmt.Sprintf("warm-%d", i)}
		err := db1.AppendDiff("escrow-1", types.DiffRecord{
			Diff: types.Diff{
				Nonce:   i,
				UserSig: []byte(fmt.Sprintf("sig-%d", i)),
			},
			StateHash:    []byte{byte(i)},
			Signatures:   map[uint32][]byte{0: {byte(i)}},
			WarmKeyDelta: delta,
			CreatedAt:    int64(i * 100),
		})
		require.NoError(t, err)

		require.NoError(t, db1.AddSignature("escrow-1", i, 1, []byte(fmt.Sprintf("host-sig-%d", i))))
	}
	require.NoError(t, db1.MarkFinalized("escrow-1", 7))
	require.NoError(t, db1.Close())

	// Phase 2: reopen and verify.
	db2, err := NewSQLite(dir)
	require.NoError(t, err)
	defer db2.Close()

	meta, err := db2.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, "escrow-1", meta.EscrowID)
	require.Equal(t, defaultParams().EpochID, meta.EpochID)
	require.Equal(t, "creator", meta.CreatorAddr)
	require.Equal(t, uint64(1000), meta.InitialBalance)
	require.Equal(t, uint64(10), meta.LatestNonce)
	require.Equal(t, uint64(7), meta.LastFinalized)
	require.Equal(t, "active", meta.Status)

	diffs, err := db2.GetDiffs("escrow-1", 1, 10)
	require.NoError(t, err)
	require.Len(t, diffs, 10)

	for i, d := range diffs {
		nonce := uint64(i + 1)
		require.Equal(t, nonce, d.Nonce)
		require.Equal(t, []byte{byte(nonce)}, d.StateHash)
		require.NotNil(t, d.WarmKeyDelta)
		expectedKey := uint32(nonce % 3)
		require.Equal(t, fmt.Sprintf("warm-%d", nonce), d.WarmKeyDelta[expectedKey])

		// Should have 2 sigs: slot 0 from AppendDiff, slot 1 from AddSignature.
		require.Len(t, d.Signatures, 2, "nonce %d", nonce)
	}

	last, err := db2.LastFinalized("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(7), last)
}

func TestSQLite_ConcurrentSessions(t *testing.T) {
	db := newTestSQLite(t)

	const numSessions = 20
	const diffsPerSession = 100

	var wg sync.WaitGroup
	wg.Add(numSessions)

	for s := 0; s < numSessions; s++ {
		go func(sessionIdx int) {
			defer wg.Done()
			escrowID := fmt.Sprintf("escrow-%d", sessionIdx)
			params := CreateSessionParams{
				EscrowID:       escrowID,
				EpochID:        7,
				Version:        storageTestVersion,
				CreatorAddr:    "creator",
				Config:         types.SessionConfig{},
				Group:          defaultGroup(),
				InitialBalance: 1000,
			}
			if err := db.CreateSession(params); err != nil {
				t.Errorf("create session %s: %v", escrowID, err)
				return
			}

			for i := uint64(1); i <= diffsPerSession; i++ {
				rec := types.DiffRecord{
					Diff: types.Diff{
						Nonce:   i,
						UserSig: []byte(fmt.Sprintf("sig-%d-%d", sessionIdx, i)),
					},
					StateHash:  []byte{byte(i)},
					Signatures: map[uint32][]byte{0: {byte(i)}},
				}
				if err := db.AppendDiff(escrowID, rec); err != nil {
					t.Errorf("append diff %s nonce %d: %v", escrowID, i, err)
					return
				}
			}
		}(s)
	}

	wg.Wait()

	// Verify each session.
	for s := 0; s < numSessions; s++ {
		escrowID := fmt.Sprintf("escrow-%d", s)
		diffs, err := db.GetDiffs(escrowID, 1, diffsPerSession)
		require.NoError(t, err, "session %s", escrowID)
		require.Len(t, diffs, diffsPerSession, "session %s", escrowID)
	}
}

func TestSQLite_ConcurrentReadWrite(t *testing.T) {
	db := newTestSQLite(t)
	require.NoError(t, db.CreateSession(defaultParams()))

	const totalDiffs = 200
	const numReaders = 5

	// Writer goroutine.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for i := uint64(1); i <= totalDiffs; i++ {
			err := db.AppendDiff("escrow-1", makeDiffRecord(i))
			if err != nil {
				t.Errorf("write diff %d: %v", i, err)
				return
			}
		}
	}()

	// Reader goroutines.
	var wg sync.WaitGroup
	wg.Add(numReaders)
	for r := 0; r < numReaders; r++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-writerDone:
					return
				default:
					_, err := db.GetDiffs("escrow-1", 1, totalDiffs)
					if err != nil {
						t.Errorf("read diffs: %v", err)
						return
					}
					_, err = db.GetSignatures("escrow-1", 1)
					if err != nil {
						// Nonce 1 might not exist yet. That's fine.
					}
				}
			}
		}()
	}

	wg.Wait()

	// Final verification.
	diffs, err := db.GetDiffs("escrow-1", 1, totalDiffs)
	require.NoError(t, err)
	require.Len(t, diffs, totalDiffs)
}

func TestSQLite_DuplicateNonce(t *testing.T) {
	db := newTestSQLite(t)
	require.NoError(t, db.CreateSession(defaultParams()))

	err := db.AppendDiff("escrow-1", makeDiffRecord(1))
	require.NoError(t, err)

	err = db.AppendDiff("escrow-1", makeDiffRecord(1))
	require.Error(t, err, "duplicate nonce should fail")

	// Verify first diff is intact.
	diffs, err := db.GetDiffs("escrow-1", 1, 1)
	require.NoError(t, err)
	require.Len(t, diffs, 1)
	require.Equal(t, uint64(1), diffs[0].Nonce)
}

func TestSQLite_LargeSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large session test in short mode")
	}

	db := newTestSQLite(t)
	require.NoError(t, db.CreateSession(defaultParams()))

	const numDiffs = 1000

	// Build diffs with all 8 tx types.
	for i := uint64(1); i <= numDiffs; i++ {
		var txs []*types.DevshardTx
		switch i % 8 {
		case 0:
			txs = append(txs, &types.DevshardTx{Tx: &types.DevshardTx_StartInference{
				StartInference: &types.MsgStartInference{InferenceId: i, Model: "test-model", InputLength: 100, MaxTokens: 50},
			}})
		case 1:
			txs = append(txs, &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{
				ConfirmStart: &types.MsgConfirmStart{InferenceId: i, ExecutorSig: []byte("exec-sig"), ConfirmedAt: int64(i)},
			}})
		case 2:
			txs = append(txs, &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{
				FinishInference: &types.MsgFinishInference{InferenceId: i, ResponseHash: []byte("resp"), InputTokens: 10, OutputTokens: 20, ExecutorSlot: 0, EscrowId: "escrow-1"},
			}})
		case 3:
			txs = append(txs, &types.DevshardTx{Tx: &types.DevshardTx_Validation{
				Validation: &types.MsgValidation{InferenceId: i, ValidatorSlot: 1, Valid: true, EscrowId: "escrow-1"},
			}})
		case 4:
			txs = append(txs, &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{
				ValidationVote: &types.MsgValidationVote{InferenceId: i, VoterSlot: 1, VoteValid: true, EscrowId: "escrow-1"},
			}})
		case 5:
			txs = append(txs, &types.DevshardTx{Tx: &types.DevshardTx_TimeoutInference{
				TimeoutInference: &types.MsgTimeoutInference{InferenceId: i, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED},
			}})
		case 6:
			txs = append(txs, &types.DevshardTx{Tx: &types.DevshardTx_RevealSeed{
				RevealSeed: &types.MsgRevealSeed{SlotId: 0, Signature: []byte("seed-sig"), EscrowId: "escrow-1"},
			}})
		case 7:
			txs = append(txs, &types.DevshardTx{Tx: &types.DevshardTx_FinalizeRound{
				FinalizeRound: &types.MsgFinalizeRound{},
			}})
		}

		rec := types.DiffRecord{
			Diff: types.Diff{
				Nonce:   i,
				Txs:     txs,
				UserSig: []byte(fmt.Sprintf("sig-%d", i)),
			},
			StateHash: []byte{byte(i % 256)},
		}
		require.NoError(t, db.AppendDiff("escrow-1", rec))
	}

	// We can't easily get the path back, so just verify from same handle.
	diffs, err := db.GetDiffs("escrow-1", 1, numDiffs)
	require.NoError(t, err)
	require.Len(t, diffs, numDiffs)

	// Verify proto round-trip: each diff should have exactly 1 tx with correct type.
	for _, d := range diffs {
		require.Len(t, d.Txs, 1, "nonce %d", d.Nonce)
		tx := d.Txs[0]
		require.NotNil(t, tx.GetTx(), "nonce %d tx should not be nil", d.Nonce)
	}
}

// TestSQLite_ReadsDuringWrite verifies that readers are not blocked by an active
// write transaction. A writer inserts many rows inside a single transaction while
// concurrent readers query the database. With separate read/write pools and WAL
// mode, readers should complete without SQLITE_BUSY errors.
func TestSQLite_ReadsDuringWrite(t *testing.T) {
	db := newTestSQLite(t)
	require.NoError(t, db.CreateSession(defaultParams()))

	// Seed one diff so readers always have something to query.
	require.NoError(t, db.AppendDiff("escrow-1", makeDiffRecord(1)))

	const (
		batchSize  = 500
		numReaders = 10
	)

	// Resolve the per-epoch pool that holds escrow-1 and reach into it.
	pool, _, err := db.poolFor("escrow-1")
	require.NoError(t, err)

	writerReady := make(chan struct{})
	writerDone := make(chan struct{})

	// Writer: hold a long transaction inserting many rows.
	go func() {
		defer close(writerDone)
		tx, err := pool.writeDB.Begin()
		if err != nil {
			t.Errorf("begin write tx: %v", err)
			return
		}
		defer tx.Rollback()

		// Signal readers to start once the transaction is open.
		close(writerReady)

		for i := uint64(2); i <= batchSize; i++ {
			_, err := tx.Exec(
				`INSERT INTO diffs (escrow_id, nonce, txs_proto, created_at) VALUES (?, ?, ?, ?)`,
				"escrow-1", i, []byte{0x0a}, int64(i),
			)
			if err != nil {
				t.Errorf("write nonce %d: %v", i, err)
				return
			}
		}

		if err := tx.Commit(); err != nil {
			t.Errorf("commit: %v", err)
		}
	}()

	// Wait for writer to open its transaction.
	<-writerReady

	var readErrors atomic.Int64
	var wg sync.WaitGroup
	wg.Add(numReaders)

	for r := 0; r < numReaders; r++ {
		go func() {
			defer wg.Done()
			// Each reader performs multiple queries while the writer holds its tx.
			for i := 0; i < 20; i++ {
				_, err := db.GetDiffs("escrow-1", 1, 1)
				if err != nil {
					readErrors.Add(1)
					t.Errorf("reader error: %v", err)
					return
				}
				_, err = db.ListActiveSessions()
				if err != nil {
					readErrors.Add(1)
					t.Errorf("reader list error: %v", err)
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}

	wg.Wait()
	<-writerDone

	require.Equal(t, int64(0), readErrors.Load(), "readers should not get errors during write tx")
}

func TestSQLite_StressMultiSessionRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	const numSessions = 50
	const diffsPerSession = 200

	dir := t.TempDir()

	// Phase 1: populate. Spread sessions across two epochs to also exercise
	// the per-epoch routing.
	db1, err := NewSQLite(dir)
	require.NoError(t, err)

	for s := 0; s < numSessions; s++ {
		escrowID := fmt.Sprintf("escrow-%d", s)
		params := CreateSessionParams{
			EscrowID:       escrowID,
			EpochID:        uint64(s % 2),
			Version:        storageTestVersion,
			CreatorAddr:    fmt.Sprintf("creator-%d", s),
			Config:         types.SessionConfig{TokenPrice: 1},
			Group:          defaultGroup(),
			InitialBalance: 1000,
		}
		require.NoError(t, db1.CreateSession(params))

		for i := uint64(1); i <= diffsPerSession; i++ {
			warmDelta := map[uint32]string{uint32(i % 5): fmt.Sprintf("warm-%d-%d", s, i)}
			rec := types.DiffRecord{
				Diff: types.Diff{
					Nonce:   i,
					UserSig: []byte(fmt.Sprintf("sig-%d-%d", s, i)),
				},
				StateHash:    []byte{byte(i % 256)},
				Signatures:   map[uint32][]byte{0: {byte(i % 256)}, 1: {byte((i + 1) % 256)}},
				WarmKeyDelta: warmDelta,
				CreatedAt:    int64(i),
			}
			require.NoError(t, db1.AppendDiff(escrowID, rec))
		}

		require.NoError(t, db1.MarkFinalized(escrowID, diffsPerSession/2))
	}

	require.NoError(t, db1.Close())

	// Phase 2: reopen and verify all data.
	db2, err := NewSQLite(dir)
	require.NoError(t, err)
	defer db2.Close()

	active, err := db2.ListActiveSessions()
	require.NoError(t, err)
	require.Len(t, active, numSessions)

	for s := 0; s < numSessions; s++ {
		escrowID := fmt.Sprintf("escrow-%d", s)

		meta, err := db2.GetSessionMeta(escrowID)
		require.NoError(t, err)
		require.Equal(t, uint64(s%2), meta.EpochID, "session %s", escrowID)
		require.Equal(t, uint64(diffsPerSession), meta.LatestNonce, "session %s", escrowID)
		require.Equal(t, uint64(diffsPerSession/2), meta.LastFinalized, "session %s", escrowID)
		require.Equal(t, "active", meta.Status)

		diffs, err := db2.GetDiffs(escrowID, 1, diffsPerSession)
		require.NoError(t, err)
		require.Len(t, diffs, diffsPerSession, "session %s", escrowID)

		for i, d := range diffs {
			nonce := uint64(i + 1)
			require.Equal(t, nonce, d.Nonce)
			require.Equal(t, []byte{byte(nonce % 256)}, d.StateHash)
			require.Len(t, d.Signatures, 2, "session %s nonce %d", escrowID, nonce)
			require.NotNil(t, d.WarmKeyDelta)
		}
	}
}

// TestSQLite_MetaIndex_PersistsAcrossReboot proves the explicit
// _meta.db sidecar is the authoritative escrow_id -> epoch_id index:
// after closing and reopening the store, all routing decisions still
// work without having to scan per-epoch sessions tables. We additionally
// verify _meta.db was actually created on disk and that the meta survives
// even if a per-epoch file is read fresh by a new process.
func TestSQLite_MetaIndex_PersistsAcrossReboot(t *testing.T) {
	dir := t.TempDir()

	db1, err := NewSQLite(dir)
	require.NoError(t, err)

	require.NoError(t, db1.CreateSession(paramsForEpoch("e7-a", 7)))
	require.NoError(t, db1.CreateSession(paramsForEpoch("e7-b", 7)))
	require.NoError(t, db1.CreateSession(paramsForEpoch("e9", 9)))
	require.NoError(t, db1.AppendDiff("e7-a", makeDiffRecord(1)))
	require.NoError(t, db1.Close())

	// Meta sidecar exists.
	_, err = os.Stat(filepath.Join(dir, "_meta.db"))
	require.NoError(t, err, "_meta.db must be present after CreateSession")

	// Reopen. Routing for every escrow must work immediately (no
	// per-epoch sessions scan required because meta carries the index).
	db2, err := NewSQLite(dir)
	require.NoError(t, err)
	defer db2.Close()

	for _, esc := range []string{"e7-a", "e7-b", "e9"} {
		meta, err := db2.GetSessionMeta(esc)
		require.NoError(t, err, "session %s should resolve via meta index", esc)
		switch esc {
		case "e7-a", "e7-b":
			require.Equal(t, uint64(7), meta.EpochID)
		case "e9":
			require.Equal(t, uint64(9), meta.EpochID)
		}
	}

	// Pruning epoch 7 must clear those rows from the meta index.
	require.NoError(t, db2.PruneEpoch(7))
	for _, esc := range []string{"e7-a", "e7-b"} {
		_, err := db2.GetSessionMeta(esc)
		require.Error(t, err, "session %s should be unrouteable after prune", esc)
	}
	// e9 still resolvable.
	_, err = db2.GetSessionMeta("e9")
	require.NoError(t, err)
}

// TestSQLite_MetaIndex_RebuildsFromEpochFiles is the disaster-recovery
// path: if _meta.db is missing or empty (e.g. operator restored only the
// epoch_*.db files from a backup, or _meta.db was corrupted), the next
// boot must rebuild the index by scanning the per-epoch sessions tables.
func TestSQLite_MetaIndex_RebuildsFromEpochFiles(t *testing.T) {
	dir := t.TempDir()

	db1, err := NewSQLite(dir)
	require.NoError(t, err)

	require.NoError(t, db1.CreateSession(paramsForEpoch("e7", 7)))
	require.NoError(t, db1.CreateSession(paramsForEpoch("e8", 8)))
	require.NoError(t, db1.AppendDiff("e7", makeDiffRecord(1)))
	require.NoError(t, db1.AppendDiff("e8", makeDiffRecord(1)))
	require.NoError(t, db1.Close())

	// Simulate _meta.db loss.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(filepath.Join(dir, "_meta.db"+suffix))
	}

	db2, err := NewSQLite(dir)
	require.NoError(t, err)
	defer db2.Close()

	// reconcile must have rebuilt the index from epoch_*.db sessions tables.
	for _, esc := range []string{"e7", "e8"} {
		meta, err := db2.GetSessionMeta(esc)
		require.NoError(t, err, "session %s recovered after meta loss", esc)
		require.Equal(t, uint64(1), meta.LatestNonce)
	}

	// And the rebuilt _meta.db is back on disk for next boot.
	_, err = os.Stat(filepath.Join(dir, "_meta.db"))
	require.NoError(t, err)
}

func TestSQLite_MetaIndex_RemovesStaleRows(t *testing.T) {
	dir := t.TempDir()

	db1, err := NewSQLite(dir)
	require.NoError(t, err)
	require.NoError(t, db1.CreateSession(paramsForEpoch("live", 7)))
	_, err = db1.metaDB.Exec(`INSERT INTO escrow_epoch (escrow_id, epoch_id) VALUES (?, ?)`, "ghost", 99)
	require.NoError(t, err)
	require.NoError(t, db1.Close())

	db2, err := NewSQLite(dir)
	require.NoError(t, err)
	defer db2.Close()

	_, err = db2.GetSessionMeta("ghost")
	require.ErrorIs(t, err, ErrSessionNotFound)

	var count int
	err = db2.metaDB.QueryRow(`SELECT COUNT(*) FROM escrow_epoch WHERE escrow_id = ?`, "ghost").Scan(&count)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestSQLite_MetaIndex_DuplicateEscrowAcrossEpochFiles(t *testing.T) {
	dir := t.TempDir()

	db1, err := NewSQLite(dir)
	require.NoError(t, err)
	require.NoError(t, db1.CreateSession(paramsForEpoch("dup", 7)))
	require.NoError(t, db1.Close())

	p, err := openEpochPool(filepath.Join(dir, "epoch_8.db"))
	require.NoError(t, err)
	_, err = p.writeDB.Exec(
		`INSERT INTO sessions (escrow_id, version, creator_addr, config_json, group_json, initial_balance)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"dup", storageTestVersion, "creator", `{}`, `[]`, 1000,
	)
	require.NoError(t, err)
	require.NoError(t, p.close())

	db2, err := NewSQLite(dir)
	require.ErrorIs(t, err, ErrSessionEpochConflict)
	if db2 != nil {
		_ = db2.Close()
	}
}

// TestSQLite_PerEpochFile_Layout verifies the on-disk layout: each epoch
// gets its own .db file under the base dir, and prune physically removes
// only that epoch's files.
func TestSQLite_PerEpochFile_Layout(t *testing.T) {
	dir := t.TempDir()
	db, err := NewSQLite(dir)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.CreateSession(paramsForEpoch("e7", 7)))
	require.NoError(t, db.CreateSession(paramsForEpoch("e8", 8)))

	// Force at least one write to each pool so WAL/shm sidecars exist.
	require.NoError(t, db.AppendDiff("e7", makeDiffRecord(1)))
	require.NoError(t, db.AppendDiff("e8", makeDiffRecord(1)))

	mustExist := func(p string) {
		t.Helper()
		_, err := os.Stat(p)
		require.NoError(t, err, "expected %s to exist", p)
	}
	mustNotExist := func(p string) {
		t.Helper()
		_, err := os.Stat(p)
		require.True(t, os.IsNotExist(err), "expected %s to be gone, got err=%v", p, err)
	}

	e7 := filepath.Join(dir, "epoch_7.db")
	e8 := filepath.Join(dir, "epoch_8.db")
	mustExist(e7)
	mustExist(e8)

	require.NoError(t, db.PruneEpoch(7))

	mustNotExist(e7)
	mustNotExist(e7 + "-wal")
	mustNotExist(e7 + "-shm")
	mustExist(e8)

	// Epoch 8 still readable.
	diffs, err := db.GetDiffs("e8", 1, 1)
	require.NoError(t, err)
	require.Len(t, diffs, 1)
}

func TestSQLite_PruneBefore_RemovesOnlyExistingOldEpochs(t *testing.T) {
	dir := t.TempDir()
	db, err := NewSQLite(dir)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.CreateSession(paramsForEpoch("e2", 2)))
	require.NoError(t, db.CreateSession(paramsForEpoch("e8", 8)))
	require.NoError(t, db.CreateSession(paramsForEpoch("e9", 9)))
	for _, esc := range []string{"e2", "e8", "e9"} {
		require.NoError(t, db.AppendDiff(esc, makeDiffRecord(1)))
	}

	require.NoError(t, db.pruneBefore(8))

	_, err = db.GetSessionMeta("e2")
	require.ErrorIs(t, err, ErrSessionNotFound)
	for _, esc := range []string{"e8", "e9"} {
		meta, err := db.GetSessionMeta(esc)
		require.NoError(t, err)
		require.Equal(t, uint64(1), meta.LatestNonce)
	}

	_, err = os.Stat(filepath.Join(dir, "epoch_2.db"))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(dir, "epoch_8.db"))
	require.NoError(t, err)
}

func TestSQLite_PruneBefore_CloseErrorStillCleansFilesAndMeta(t *testing.T) {
	dir := t.TempDir()
	db, err := NewSQLite(dir)
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.CreateSession(paramsForEpoch("old-1", 1)))
	require.NoError(t, db.CreateSession(paramsForEpoch("old-2", 2)))
	require.NoError(t, db.CreateSession(paramsForEpoch("recent", 3)))
	for _, esc := range []string{"old-1", "old-2", "recent"} {
		require.NoError(t, db.AppendDiff(esc, makeDiffRecord(1)))
	}

	db.mu.RLock()
	pool := db.pools[1]
	db.mu.RUnlock()
	require.NotNil(t, pool)
	pool.writeDB = nil

	err = db.pruneBefore(3)
	require.Error(t, err)
	require.Contains(t, err.Error(), "close epoch 1 pool")

	for _, epoch := range []uint64{1, 2} {
		require.NoFileExists(t, filepath.Join(dir, fmt.Sprintf("epoch_%d.db", epoch)))
		require.NoFileExists(t, filepath.Join(dir, fmt.Sprintf("epoch_%d.db-wal", epoch)))
		require.NoFileExists(t, filepath.Join(dir, fmt.Sprintf("epoch_%d.db-shm", epoch)))
	}
	require.FileExists(t, filepath.Join(dir, "epoch_3.db"))

	var oldMetaRows int
	require.NoError(t, db.metaDB.QueryRow(`SELECT COUNT(*) FROM escrow_epoch WHERE epoch_id < 3`).Scan(&oldMetaRows))
	require.Zero(t, oldMetaRows)
	_, err = db.GetSessionMeta("old-1")
	require.ErrorIs(t, err, ErrSessionNotFound)
	_, err = db.GetSessionMeta("old-2")
	require.ErrorIs(t, err, ErrSessionNotFound)
	_, err = db.GetSessionMeta("recent")
	require.NoError(t, err)

	require.NoError(t, db.pruneBefore(3), "second prune should not retry closed pools")
}
