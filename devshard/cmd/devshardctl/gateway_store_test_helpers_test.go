package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestGatewayStore(t *testing.T, backend string) GatewayStore {
	t.Helper()
	switch backend {
	case "sqlite":
		store, err := NewSQLiteGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, store.Close())
		})
		return store
	case "postgres":
		return newTestPostgresGatewayStore(t)
	default:
		t.Fatalf("unsupported test gateway backend %q", backend)
		return nil
	}
}

func requireSQLiteGatewayStore(t *testing.T, store GatewayStore) *SQLiteGatewayStore {
	t.Helper()
	sqliteStore, ok := store.(*SQLiteGatewayStore)
	require.True(t, ok, "expected *SQLiteGatewayStore")
	return sqliteStore
}
