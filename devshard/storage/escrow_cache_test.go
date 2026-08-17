package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemory_EscrowCache_RoundTrip(t *testing.T) {
	store := NewMemory()
	info := EscrowCacheInfo{
		EscrowID:       "escrow-1",
		Amount:         42,
		CreatorAddress: "creator",
		Slots:          []string{"a", "b", "c"},
		TokenPrice:     7,
		EpochID:        9,
		AppHash:        []byte{1, 2, 3},
	}
	require.NoError(t, store.PutEscrowCache(info))

	got, err := store.GetEscrowCache("escrow-1")
	require.NoError(t, err)
	require.Equal(t, info.EscrowID, got.EscrowID)
	require.Equal(t, info.Amount, got.Amount)
	require.Equal(t, info.Slots, got.Slots)
	require.Equal(t, info.EpochID, got.EpochID)
	require.Equal(t, info.AppHash, got.AppHash)

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Empty(t, active, "cache must not create a session")

	require.NoError(t, store.DeleteEscrowCache("escrow-1"))
	_, err = store.GetEscrowCache("escrow-1")
	require.ErrorIs(t, err, ErrEscrowCacheNotFound)
	require.NoError(t, store.DeleteEscrowCache("escrow-1"), "delete is idempotent")
}

func TestMemory_EscrowCache_PruneEpoch(t *testing.T) {
	store := NewMemory()
	require.NoError(t, store.PutEscrowCache(EscrowCacheInfo{EscrowID: "old", EpochID: 5, Amount: 1}))
	require.NoError(t, store.PutEscrowCache(EscrowCacheInfo{EscrowID: "new", EpochID: 7, Amount: 1}))
	require.NoError(t, store.PruneEpoch(5))
	_, err := store.GetEscrowCache("old")
	require.ErrorIs(t, err, ErrEscrowCacheNotFound)
	_, err = store.GetEscrowCache("new")
	require.NoError(t, err)
}

func TestSQLite_EscrowCache_RoundTrip(t *testing.T) {
	store := newTestSQLite(t)
	info := EscrowCacheInfo{
		EscrowID:       "escrow-sql",
		Amount:         99,
		CreatorAddress: "creator",
		Slots:          []string{"x", "y", "z"},
		EpochID:        3,
	}
	require.NoError(t, store.PutEscrowCache(info))
	got, err := store.GetEscrowCache("escrow-sql")
	require.NoError(t, err)
	require.Equal(t, info, *got)

	active, err := store.ListActiveSessions()
	require.NoError(t, err)
	require.Empty(t, active)

	require.NoError(t, store.PruneEpoch(3))
	_, err = store.GetEscrowCache("escrow-sql")
	require.ErrorIs(t, err, ErrEscrowCacheNotFound)
}
