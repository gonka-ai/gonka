package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewGatewayStorePGHOSTUnset(t *testing.T) {
	t.Setenv("PGHOST", "")

	ctx := context.Background()
	store, err := NewGatewayStore(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	_, ok := store.(*SQLiteGatewayStore)
	require.True(t, ok, "expected *SQLiteGatewayStore when PGHOST is unset")
}

func TestNewGatewayStorePGHOSTSetPGUp(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	store, err := NewGatewayStore(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	hybrid, ok := store.(*HybridGatewayStore)
	require.True(t, ok)
	require.NotNil(t, hybrid.currentPg())

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 2048,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}.WithTuningDefaults()
	require.NoError(t, store.Initialize(settings, nil))

	pgState, has, err := hybrid.currentPg().LoadState()
	require.NoError(t, err)
	require.True(t, has)
	require.EqualValues(t, 2048, pgState.Settings.DefaultRequestMaxTokens)
}

func TestNewGatewayStorePGHOSTSetPGDown(t *testing.T) {
	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGPORT", "1")
	t.Setenv("PG_CONNECT_TIMEOUT", "100ms")

	ctx := context.Background()
	store, err := NewGatewayStore(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	hybrid, ok := store.(*HybridGatewayStore)
	require.True(t, ok)
	require.Nil(t, hybrid.currentPg())

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1111,
		MaxConcurrentRequests:   1,
		MaxInputTokensInFlight:  100,
	}.WithTuningDefaults()
	require.NoError(t, store.Initialize(settings, nil))

	sqliteState, has, err := hybrid.sqlite.LoadState()
	require.NoError(t, err)
	require.True(t, has)
	require.EqualValues(t, 1111, sqliteState.Settings.DefaultRequestMaxTokens)
}

func TestNewGatewayStoreAutoMigration(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	baseDir := t.TempDir()
	sqlite, err := NewSQLiteGatewayStore(filepath.Join(baseDir, "gateway.db"))
	require.NoError(t, err)
	seedGatewayStoreForMigration(t, sqlite)
	require.NoError(t, sqlite.Close())

	ctx := context.Background()
	store, err := NewGatewayStore(ctx, baseDir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	hybrid, ok := store.(*HybridGatewayStore)
	require.True(t, ok)
	require.NotNil(t, hybrid.currentPg())

	_, err = os.Stat(filepath.Join(baseDir, "gateway.db"))
	require.NoError(t, err)

	pgState, has, err := hybrid.currentPg().LoadState()
	require.NoError(t, err)
	require.True(t, has)
	require.EqualValues(t, 1500, pgState.Settings.DefaultRequestMaxTokens)
	require.Len(t, pgState.Devshards, 2)
}
