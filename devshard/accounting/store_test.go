package accounting

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"devshard/types"

	"github.com/stretchr/testify/require"
)

func TestStoreRecoveryRetentionAndAtomicSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.db")
	store, err := OpenStore(path, 1)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	var journalMode string
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode))
	require.Equal(t, "wal", journalMode)
	require.NoError(t, db.Close())

	book := NewBook()
	registerTestEscrow(t, book, "old-complete", 1, "model-a", EscrowSettled)
	registerTestEscrow(t, book, "old-incomplete", 2, "model-a", EscrowActive)
	registerTestEscrow(t, book, "current", 3, "model-b", EscrowActive)
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "old-incomplete", Nonce: 1}))
	require.NoError(t, book.Apply(HostStatsObserved{
		EscrowID: "old-incomplete",
		SlotID:   1,
		Stats:    types.HostStats{Missed: 1, Invalid: 2, Cost: 3},
	}))
	require.NoError(t, store.Snapshot(context.Background(), book))
	require.Empty(t, book.Query(QueryFilter{EpochIndex: 1}))

	recovered, err := store.Load(context.Background())
	require.NoError(t, err)
	epochs := recovered.Epochs(QueryFilter{})
	require.Len(t, epochs, 2)
	require.Equal(t, uint64(2), epochs[0].EpochIndex)
	require.Equal(t, uint64(3), epochs[1].EpochIndex)
	require.Equal(t, uint64(1), epochs[0].Dispositions[DispositionProtocolOnly])
	recoveredRecord := participantRecord(t, recovered.Query(QueryFilter{EpochIndex: 2}), "participant-1")
	require.Equal(t, uint64(1), recoveredRecord.ProtocolMisses)
	require.Equal(t, uint64(2), recoveredRecord.ProtocolInvalid)

	require.NoError(t, book.Apply(DiffApplied{EscrowID: "current", Nonce: 1}))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, store.Snapshot(canceled, book))

	stillPrevious, err := store.Load(context.Background())
	require.NoError(t, err)
	current := stillPrevious.Query(QueryFilter{EpochIndex: 3})
	require.NotEmpty(t, current)
	for _, record := range current {
		require.Zero(t, record.AssignedNonces)
	}
}

func TestStorePersistsRetryableTimeoutState(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "accounting.db"), 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	book := NewBook()
	registerTestEscrow(t, book, "escrow-1", 4, "model-a", EscrowActive)
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 1, Inference: true}))
	require.NoError(t, book.Apply(RealSend{EscrowID: "escrow-1", Nonce: 1}))
	require.NoError(t, book.Apply(Loser{EscrowID: "escrow-1", Nonce: 1}))
	require.NoError(t, book.Apply(TimeoutRequired{
		EscrowID: "escrow-1", Nonce: 1, Kind: TimeoutRefused,
		FailureOrigin: FailureHostResponse, DetailReason: "no_receipt",
	}))
	require.NoError(t, book.Apply(TimeoutOutcomeRecorded{
		EscrowID: "escrow-1", Nonce: 1, Outcome: TimeoutDiffSendFailed,
	}))
	require.NoError(t, store.Snapshot(context.Background(), book))

	recovered, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, recovered.LiveNonceCount())
	require.NoError(t, recovered.Apply(ProtocolTransition{
		EscrowID: "escrow-1", Nonce: 1, Kind: ProtocolFinishApplied,
	}))
	record := participantRecord(t, recovered.Query(QueryFilter{EpochIndex: 4}), "participant-1")
	require.Equal(t, uint64(1), record.Dispositions[DispositionFinishedUnused])
}
