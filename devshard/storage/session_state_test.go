package storage

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

type sessionStateBackend interface {
	Storage
	SessionStateStore
}

func sessionStateDiff(nonce uint64, delta *types.SessionStateDelta) types.DiffRecord {
	return types.DiffRecord{
		Diff:         types.Diff{Nonce: nonce, UserSig: []byte{byte(nonce)}, PostStateRoot: []byte{byte(nonce)}},
		StateHash:    []byte{byte(nonce)},
		SessionState: delta,
	}
}

// Test flow:
//  1. On SQLite and on the memory store, append a diff with no state, then one that replaces the live set.
//  2. Append diffs that upsert, delete, and append nothing to the state.
//  3. After each step the loaded state holds exactly the expected entries, header, and nonce.
func TestSessionStateFollowsDiffs(t *testing.T) {
	backends := []struct {
		name  string
		store func(t *testing.T) sessionStateBackend
	}{
		{name: "sqlite", store: func(t *testing.T) sessionStateBackend { return newTestSQLite(t) }},
		{name: "memory", store: func(t *testing.T) sessionStateBackend { return NewMemory() }},
	}
	for _, backend := range backends {
		t.Run(backend.name, func(t *testing.T) {
			store := backend.store(t)
			require.NoError(t, store.CreateSession(defaultParams()))
			escrowID := defaultParams().EscrowID

			require.NoError(t, store.AppendDiff(escrowID, sessionStateDiff(1, nil)))
			_, err := store.LoadSessionState(escrowID)
			require.ErrorIs(t, err, ErrSessionStateNotFound, "a diff without state writes none")

			require.NoError(t, store.AppendDiff(escrowID, sessionStateDiff(2, &types.SessionStateDelta{
				Header: []byte("header-2"), Upserts: map[uint64][]byte{1: []byte("one"), 2: []byte("two")}, ReplaceAll: true,
			})))
			requireSessionState(t, store, escrowID, 2, "header-2", map[uint64][]byte{1: []byte("one"), 2: []byte("two")})

			require.NoError(t, store.AppendDiff(escrowID, sessionStateDiff(3, &types.SessionStateDelta{
				Header: []byte("header-3"), Upserts: map[uint64][]byte{2: []byte("two-v2"), 3: []byte("three")}, Deletes: []uint64{1},
			})))
			requireSessionState(t, store, escrowID, 3, "header-3", map[uint64][]byte{2: []byte("two-v2"), 3: []byte("three")})

			require.NoError(t, store.AppendDiff(escrowID, sessionStateDiff(4, &types.SessionStateDelta{Header: []byte("header-4")})))
			requireSessionState(t, store, escrowID, 4, "header-4", map[uint64][]byte{2: []byte("two-v2"), 3: []byte("three")})

			require.NoError(t, store.AppendDiff(escrowID, sessionStateDiff(5, &types.SessionStateDelta{
				Header: []byte("header-5"), Upserts: map[uint64][]byte{9: []byte("nine")}, ReplaceAll: true,
			})))
			requireSessionState(t, store, escrowID, 5, "header-5", map[uint64][]byte{9: []byte("nine")})
		})
	}
}

func requireSessionState(t *testing.T, store SessionStateStore, escrowID string, nonce uint64, header string, entries map[uint64][]byte) {
	t.Helper()
	stored, err := store.LoadSessionState(escrowID)
	require.NoError(t, err)
	require.Equal(t, nonce, stored.Nonce)
	require.Equal(t, header, string(stored.Header))
	require.Equal(t, entries, stored.Entries)
}
