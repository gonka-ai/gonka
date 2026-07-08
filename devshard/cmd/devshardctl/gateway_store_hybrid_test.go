package main

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestSQLiteGatewayStoreOnly(t *testing.T) *SQLiteGatewayStore {
	t.Helper()
	store, err := NewSQLiteGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func TestHybridGatewayStoreImplementsInterface(t *testing.T) {
	var _ GatewayStore = (*HybridGatewayStore)(nil)
}

func TestHybridGatewayStorePGPrimaryWriteRead(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	pg, err := NewPostgresGatewayStore(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Close()) })

	sqlite := newTestSQLiteGatewayStoreOnly(t)
	hybrid := NewHybridGatewayStore(pg, sqlite, time.Second, gatewayPGConnectTimeout)
	t.Cleanup(func() { require.NoError(t, hybrid.Close()) })

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 2048,
		MaxConcurrentRequests:   4,
		MaxInputTokensInFlight:  400,
	}.WithTuningDefaults()
	require.NoError(t, hybrid.Initialize(settings, nil))

	updated := settings
	updated.DefaultRequestMaxTokens = 4096
	require.NoError(t, hybrid.UpdateSettings(updated))

	pgState, pgHas, err := pg.LoadState()
	require.NoError(t, err)
	require.True(t, pgHas)
	require.EqualValues(t, 4096, pgState.Settings.DefaultRequestMaxTokens)

	hybridState, hybridHas, err := hybrid.LoadState()
	require.NoError(t, err)
	require.True(t, hybridHas)
	require.Equal(t, pgState.Settings, hybridState.Settings)
}

func TestHybridGatewayStoreWriteFallbackOnPGDown(t *testing.T) {
	sqlite := newTestSQLiteGatewayStoreOnly(t)
	hybrid := NewHybridGatewayStore(nil, sqlite, time.Hour, gatewayPGConnectTimeout)
	hybrid.connectPG = func(context.Context) (*PostgresGatewayStore, error) {
		return nil, errGatewayStoreUnavailable
	}
	t.Cleanup(func() { require.NoError(t, hybrid.Close()) })

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}.WithTuningDefaults()
	require.NoError(t, hybrid.Initialize(settings, nil))

	sqliteState, sqliteHas, err := sqlite.LoadState()
	require.NoError(t, err)
	require.True(t, sqliteHas)
	require.Equal(t, settings.DefaultRequestMaxTokens, sqliteState.Settings.DefaultRequestMaxTokens)
}

func TestHybridGatewayStoreReadFallbackFromSQLite(t *testing.T) {
	sqlite := newTestSQLiteGatewayStoreOnly(t)
	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 777,
		MaxConcurrentRequests:   3,
		MaxInputTokensInFlight:  300,
	}.WithTuningDefaults()
	require.NoError(t, sqlite.Initialize(settings, nil))

	hybrid := NewHybridGatewayStore(nil, sqlite, time.Hour, gatewayPGConnectTimeout)
	hybrid.connectPG = func(context.Context) (*PostgresGatewayStore, error) {
		return nil, errGatewayStoreUnavailable
	}
	t.Cleanup(func() { require.NoError(t, hybrid.Close()) })

	state, has, err := hybrid.LoadState()
	require.NoError(t, err)
	require.True(t, has)
	require.EqualValues(t, 777, state.Settings.DefaultRequestMaxTokens)
}

func TestHybridGatewayStoreLazyReconnect(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	sqlite := newTestSQLiteGatewayStoreOnly(t)
	hybrid := NewHybridGatewayStore(nil, sqlite, time.Millisecond, gatewayPGConnectTimeout)
	t.Cleanup(func() { require.NoError(t, hybrid.Close()) })

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 3333,
		MaxConcurrentRequests:   1,
		MaxInputTokensInFlight:  100,
	}.WithTuningDefaults()
	require.NoError(t, hybrid.Initialize(settings, nil))

	require.NotNil(t, hybrid.currentPg())
	pgState, pgHas, err := hybrid.currentPg().LoadState()
	require.NoError(t, err)
	require.True(t, pgHas)
	require.EqualValues(t, 3333, pgState.Settings.DefaultRequestMaxTokens)
}

func TestHybridGatewayStoreReconnectRateLimit(t *testing.T) {
	sqlite := newTestSQLiteGatewayStoreOnly(t)
	hybrid := NewHybridGatewayStore(nil, sqlite, time.Hour, gatewayPGConnectTimeout)
	var attempts atomic.Int32
	hybrid.connectPG = func(context.Context) (*PostgresGatewayStore, error) {
		attempts.Add(1)
		return nil, errGatewayStoreUnavailable
	}
	t.Cleanup(func() { require.NoError(t, hybrid.Close()) })

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   1,
		MaxInputTokensInFlight:  100,
	}.WithTuningDefaults()

	require.NoError(t, sqlite.Initialize(settings, nil))

	require.NoError(t, hybrid.SaveRotationStatus(GatewayRotationStatus{
		ModelID: "Qwen/Test", Stage: "prepare_temp", Epoch: 1, Completed: true,
	}))
	require.NoError(t, hybrid.SaveRotationStatus(GatewayRotationStatus{
		ModelID: "Kimi/Rotate", Stage: "prepare_temp", Epoch: 2, Completed: true,
	}))
	require.EqualValues(t, 1, attempts.Load())
}
