package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// gatewayStoreTestBackends lists the backends that backend-agnostic store
// contract tests run against. The "postgres" backend is skipped in -short mode
// (it needs Docker) via setupPostgresContainer.
var gatewayStoreTestBackends = []string{"sqlite", "postgres"}

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
