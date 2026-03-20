package storage

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"subnet/types"
)

func newTestSQLite(t *testing.T) *SQLite {
	t.Helper()
	db, err := NewSQLite(filepath.Join(t.TempDir(), "test.db"))
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

func TestSQLite_AppendDiff_GetDiffs(t *testing.T) {
	runAppendDiff_GetDiffs(t, newTestSQLite(t))
}

func TestSQLite_GetSignatures(t *testing.T) {
	runGetSignatures(t, newTestSQLite(t))
}

func TestSQLite_MarkFinalized_LastFinalized(t *testing.T) {
	runMarkFinalized_LastFinalized(t, newTestSQLite(t))
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

// SQLite-specific durability tests.

func TestSQLite_PersistAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "persist.db")

	// Phase 1: write data.
	db1, err := NewSQLite(dbPath)
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
	db2, err := NewSQLite(dbPath)
	require.NoError(t, err)
	defer db2.Close()

	meta, err := db2.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, "escrow-1", meta.EscrowID)
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
		var txs []*types.SubnetTx
		switch i % 8 {
		case 0:
			txs = append(txs, &types.SubnetTx{Tx: &types.SubnetTx_StartInference{
				StartInference: &types.MsgStartInference{InferenceId: i, Model: "test-model", InputLength: 100, MaxTokens: 50},
			}})
		case 1:
			txs = append(txs, &types.SubnetTx{Tx: &types.SubnetTx_ConfirmStart{
				ConfirmStart: &types.MsgConfirmStart{InferenceId: i, ExecutorSig: []byte("exec-sig"), ConfirmedAt: int64(i)},
			}})
		case 2:
			txs = append(txs, &types.SubnetTx{Tx: &types.SubnetTx_FinishInference{
				FinishInference: &types.MsgFinishInference{InferenceId: i, ResponseHash: []byte("resp"), InputTokens: 10, OutputTokens: 20, ExecutorSlot: 0, EscrowId: "escrow-1"},
			}})
		case 3:
			txs = append(txs, &types.SubnetTx{Tx: &types.SubnetTx_Validation{
				Validation: &types.MsgValidation{InferenceId: i, ValidatorSlot: 1, Valid: true, EscrowId: "escrow-1"},
			}})
		case 4:
			txs = append(txs, &types.SubnetTx{Tx: &types.SubnetTx_ValidationVote{
				ValidationVote: &types.MsgValidationVote{InferenceId: i, VoterSlot: 1, VoteValid: true, EscrowId: "escrow-1"},
			}})
		case 5:
			txs = append(txs, &types.SubnetTx{Tx: &types.SubnetTx_TimeoutInference{
				TimeoutInference: &types.MsgTimeoutInference{InferenceId: i, Reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED},
			}})
		case 6:
			txs = append(txs, &types.SubnetTx{Tx: &types.SubnetTx_RevealSeed{
				RevealSeed: &types.MsgRevealSeed{SlotId: 0, Signature: []byte("seed-sig"), EscrowId: "escrow-1"},
			}})
		case 7:
			txs = append(txs, &types.SubnetTx{Tx: &types.SubnetTx_FinalizeRound{
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

	writerReady := make(chan struct{})
	writerDone := make(chan struct{})

	// Writer: hold a long transaction inserting many rows.
	go func() {
		defer close(writerDone)
		tx, err := db.writeDB.Begin()
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

	dbPath := filepath.Join(t.TempDir(), "stress.db")

	// Phase 1: populate.
	db1, err := NewSQLite(dbPath)
	require.NoError(t, err)

	for s := 0; s < numSessions; s++ {
		escrowID := fmt.Sprintf("escrow-%d", s)
		params := CreateSessionParams{
			EscrowID:       escrowID,
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
	db2, err := NewSQLite(dbPath)
	require.NoError(t, err)
	defer db2.Close()

	active, err := db2.ListActiveSessions()
	require.NoError(t, err)
	require.Len(t, active, numSessions)

	for s := 0; s < numSessions; s++ {
		escrowID := fmt.Sprintf("escrow-%d", s)

		meta, err := db2.GetSessionMeta(escrowID)
		require.NoError(t, err)
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
