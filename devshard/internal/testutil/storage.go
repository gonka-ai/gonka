package testutil

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/storage"
	"devshard/types"
)

// MustMemoryStore opens an in-memory devshard store with a session row for tests.
func MustMemoryStore(
	t *testing.T,
	escrowID string,
	creatorAddr string,
	config types.SessionConfig,
	group []types.SlotAssignment,
	initialBalance uint64,
) *storage.Memory {
	t.Helper()
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID:       escrowID,
		EpochID:        1,
		Version:        RuntimeTestVersion,
		CreatorAddr:    creatorAddr,
		Config:         config,
		Group:          group,
		InitialBalance: initialBalance,
	}))
	return store
}
