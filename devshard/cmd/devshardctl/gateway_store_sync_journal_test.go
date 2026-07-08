package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCoalesceSyncJournalRows(t *testing.T) {
	rows := []gatewaySyncJournalRow{
		{seq: 1, tableName: gatewayTableDevshards, rowKey: "a", op: gatewaySyncOpUpsert},
		{seq: 2, tableName: gatewayTableDevshards, rowKey: "a", op: gatewaySyncOpUpsert},
		{seq: 3, tableName: gatewayTableDevshards, rowKey: "a", op: gatewaySyncOpDelete},
		{seq: 4, tableName: gatewayTableDevshards, rowKey: "b", op: gatewaySyncOpDelete},
		{seq: 5, tableName: gatewayTableDevshards, rowKey: "b", op: gatewaySyncOpUpsert},
	}
	coalesced := coalesceSyncJournalRows(rows)
	require.Equal(t, gatewaySyncOpDelete, coalesced[gatewaySyncKey{tableName: gatewayTableDevshards, rowKey: "a"}])
	require.Equal(t, gatewaySyncOpUpsert, coalesced[gatewaySyncKey{tableName: gatewayTableDevshards, rowKey: "b"}])
}

func TestHybridSyncJournalOnFallback(t *testing.T) {
	sqlite := newTestSQLiteGatewayStoreOnly(t)
	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}.WithTuningDefaults()
	require.NoError(t, sqlite.Initialize(settings, nil))

	hybrid := NewHybridGatewayStore(nil, sqlite, timeHour, gatewayPGConnectTimeout)
	hybrid.connectPG = func(context.Context) (*PostgresGatewayStore, error) {
		return nil, errGatewayStoreUnavailable
	}
	t.Cleanup(func() { require.NoError(t, hybrid.Close()) })

	updated := settings
	updated.DefaultRequestMaxTokens = 2222
	require.NoError(t, hybrid.UpdateSettings(updated))

	count, err := sqlite.countSyncJournalEntries()
	require.NoError(t, err)
	require.Equal(t, 1, count)

	rows, err := sqlite.loadSyncJournalUpTo(10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, gatewayTableSettings, rows[0].tableName)
	require.Equal(t, gatewaySettingsRowKey, rows[0].rowKey)
	require.Equal(t, gatewaySyncOpUpsert, rows[0].op)
}

func TestHybridSyncJournalAtomicWrite(t *testing.T) {
	sqlite := newTestSQLiteGatewayStoreOnly(t)
	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}.WithTuningDefaults()
	require.NoError(t, sqlite.Initialize(settings, nil))

	err := sqlite.writeWithSyncJournal(
		[]gatewaySyncEntry{{tableName: gatewayTableSettings, rowKey: gatewaySettingsRowKey, op: gatewaySyncOpUpsert}},
		func(tx *sql.Tx) error { return fmt.Errorf("forced sqlite write failure") },
	)
	require.Error(t, err)

	count, err := sqlite.countSyncJournalEntries()
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestHybridSyncJournalKillSwitch(t *testing.T) {
	t.Setenv("GATEWAY_PG_SYNC_JOURNAL", "false")

	sqlite := newTestSQLiteGatewayStoreOnly(t)
	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}.WithTuningDefaults()
	require.NoError(t, sqlite.Initialize(settings, nil))

	hybrid := NewHybridGatewayStore(nil, sqlite, timeHour, gatewayPGConnectTimeout)
	hybrid.connectPG = func(context.Context) (*PostgresGatewayStore, error) {
		return nil, errGatewayStoreUnavailable
	}
	t.Cleanup(func() { require.NoError(t, hybrid.Close()) })

	updated := settings
	updated.DefaultRequestMaxTokens = 3333
	require.NoError(t, hybrid.UpdateSettings(updated))

	count, err := sqlite.countSyncJournalEntries()
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestSyncJournalRestartPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.db")

	sqlite, err := NewSQLiteGatewayStore(path)
	require.NoError(t, err)
	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   1,
		MaxInputTokensInFlight:  100,
	}.WithTuningDefaults()
	require.NoError(t, sqlite.Initialize(settings, nil))

	hybrid := NewHybridGatewayStore(nil, sqlite, timeHour, gatewayPGConnectTimeout)
	hybrid.connectPG = func(context.Context) (*PostgresGatewayStore, error) {
		return nil, errGatewayStoreUnavailable
	}
	require.NoError(t, hybrid.UpdateSettings(settings))
	require.NoError(t, hybrid.Close())
	require.NoError(t, sqlite.Close())

	reopened, err := NewSQLiteGatewayStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	count, err := reopened.countSyncJournalEntries()
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestSyncJournalDrainRestoresOutageDeltas(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	pg, err := NewPostgresGatewayStore(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Close()) })

	sqlite := newTestSQLiteGatewayStoreOnly(t)
	base := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}.WithTuningDefaults()
	require.NoError(t, pg.Initialize(base, []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{ID: "keep-me", Model: "Qwen/Test", StoragePath: "/data/keep"},
		Active:        true,
	}}))

	hybrid := NewHybridGatewayStore(nil, sqlite, timeHour, gatewayPGConnectTimeout)
	hybrid.connectPG = func(context.Context) (*PostgresGatewayStore, error) {
		return nil, errGatewayStoreUnavailable
	}
	require.NoError(t, sqlite.Initialize(base, []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{ID: "keep-me", Model: "Qwen/Test", StoragePath: "/data/keep"},
		Active:        true,
	}, {
		RuntimeConfig: RuntimeConfig{ID: "outage-only", Model: "Kimi/Rotate", StoragePath: "/data/outage"},
		Active:        true,
	}}))
	outageSettings := base
	outageSettings.DefaultRequestMaxTokens = 5555
	require.NoError(t, hybrid.UpdateSettings(outageSettings))
	require.NoError(t, hybrid.UpsertDevshard(GatewayDevshardState{
		RuntimeConfig: RuntimeConfig{ID: "outage-only", Model: "Kimi/Rotate", StoragePath: "/data/outage-new"},
		Active:        true,
	}))
	require.NoError(t, hybrid.DeleteDevshard("keep-me"))

	drainHybrid := NewHybridGatewayStore(nil, sqlite, timeHour, gatewayPGConnectTimeout)
	require.NoError(t, drainHybrid.ReconcileSyncJournal(ctx, pg))

	pgState, has, err := pg.LoadState()
	require.NoError(t, err)
	require.True(t, has)
	require.EqualValues(t, 5555, pgState.Settings.DefaultRequestMaxTokens)
	require.Len(t, pgState.Devshards, 1)
	require.Equal(t, "outage-only", pgState.Devshards[0].ID)
	require.Equal(t, "/data/outage-new", pgState.Devshards[0].StoragePath)

	count, err := sqlite.countSyncJournalEntries()
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestSyncJournalDrainNoClobberPGOnlyRows(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	pg, err := NewPostgresGatewayStore(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Close()) })

	sqlite := newTestSQLiteGatewayStoreOnly(t)
	pgOnly := GatewaySettings{
		ChainREST:               "http://pg-only:1317",
		PublicAPI:               "http://pg-only:9000",
		DefaultModel:            "PG/Only",
		DefaultRequestMaxTokens: 9999,
		MaxConcurrentRequests:   9,
		MaxInputTokensInFlight:  900,
	}.WithTuningDefaults()
	require.NoError(t, pg.Initialize(pgOnly, nil))

	staleSQLite := GatewaySettings{
		ChainREST:               "http://stale:1317",
		PublicAPI:               "http://stale:9000",
		DefaultModel:            "Stale/Model",
		DefaultRequestMaxTokens: 1,
		MaxConcurrentRequests:   1,
		MaxInputTokensInFlight:  1,
	}.WithTuningDefaults()
	require.NoError(t, sqlite.Initialize(staleSQLite, nil))

	hybrid := NewHybridGatewayStore(nil, sqlite, timeHour, gatewayPGConnectTimeout)
	hybrid.connectPG = func(context.Context) (*PostgresGatewayStore, error) {
		return nil, errGatewayStoreUnavailable
	}
	require.NoError(t, hybrid.SaveRotationStatus(GatewayRotationStatus{
		ModelID: "Qwen/Test", Stage: "prepare_temp", Epoch: 3, Completed: true,
	}))

	drainHybrid := NewHybridGatewayStore(nil, sqlite, timeHour, gatewayPGConnectTimeout)
	require.NoError(t, drainHybrid.ReconcileSyncJournal(ctx, pg))

	pgState, has, err := pg.LoadState()
	require.NoError(t, err)
	require.True(t, has)
	require.Equal(t, pgOnly, pgState.Settings)

	statuses, err := pg.LoadRotationStatuses(0)
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	require.Equal(t, "prepare_temp", statuses[0].Stage)
}

func TestSyncJournalDrainIdempotent(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	pg, err := NewPostgresGatewayStore(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Close()) })

	sqlite := newTestSQLiteGatewayStoreOnly(t)
	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   1,
		MaxInputTokensInFlight:  100,
	}.WithTuningDefaults()
	require.NoError(t, sqlite.Initialize(settings, nil))

	hybrid := NewHybridGatewayStore(nil, sqlite, timeHour, gatewayPGConnectTimeout)
	hybrid.connectPG = func(context.Context) (*PostgresGatewayStore, error) {
		return nil, errGatewayStoreUnavailable
	}
	require.NoError(t, hybrid.UpdateSettings(settings))

	drainHybrid := NewHybridGatewayStore(nil, sqlite, timeHour, gatewayPGConnectTimeout)
	require.NoError(t, drainHybrid.ReconcileSyncJournal(ctx, pg))
	require.NoError(t, drainHybrid.ReconcileSyncJournal(ctx, pg))

	count, err := sqlite.countSyncJournalEntries()
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

// TestHybridSuspiciousHostsFallbackNoDeadlock guards against reading the
// suspicious hosts via a fresh s.db query inside writeWithSyncJournal's open
// transaction. SQLite is capped at one connection, so that would deadlock.
func TestHybridSuspiciousHostsFallbackNoDeadlock(t *testing.T) {
	sqlite := newTestSQLiteGatewayStoreOnly(t)
	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   1,
		MaxInputTokensInFlight:  100,
	}.WithTuningDefaults()
	require.NoError(t, sqlite.Initialize(settings, nil))

	hybrid := NewHybridGatewayStore(nil, sqlite, timeHour, gatewayPGConnectTimeout)
	hybrid.connectPG = func(context.Context) (*PostgresGatewayStore, error) {
		return nil, errGatewayStoreUnavailable
	}
	t.Cleanup(func() { require.NoError(t, hybrid.Close()) })

	runWithTimeout(t, "UpsertSuspiciousHosts", func() {
		hosts, err := hybrid.UpsertSuspiciousHosts([]string{"pk1", "pk2"}, "flagged")
		require.NoError(t, err)
		require.Len(t, hosts, 2)
	})

	count, err := sqlite.countSyncJournalEntries()
	require.NoError(t, err)
	require.Equal(t, 2, count)

	runWithTimeout(t, "DeleteSuspiciousHosts", func() {
		hosts, err := hybrid.DeleteSuspiciousHosts([]string{"pk1"})
		require.NoError(t, err)
		require.Len(t, hosts, 1)
	})
}

func runWithTimeout(t *testing.T, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s deadlocked in SQLite fallback", name)
	}
}

// TestHybridDropPgClearsCachedConnection verifies a failed Postgres write drops
// the cached connection so subsequent writes journal and the next reconnect
// re-drains the journal before republishing Postgres as primary.
func TestHybridDropPgClearsCachedConnection(t *testing.T) {
	sqlite := newTestSQLiteGatewayStoreOnly(t)
	hybrid := NewHybridGatewayStore(nil, sqlite, timeHour, gatewayPGConnectTimeout)
	t.Cleanup(func() { require.NoError(t, hybrid.Close()) })

	pg := &PostgresGatewayStore{}
	hybrid.mu.Lock()
	hybrid.pg = pg
	hybrid.mu.Unlock()

	// Dropping a stale pointer must not clear a newer connection.
	hybrid.dropPg(&PostgresGatewayStore{})
	require.Equal(t, pg, hybrid.currentPg())

	// Dropping the live connection clears it and is idempotent.
	hybrid.dropPg(pg)
	require.Nil(t, hybrid.currentPg())
	hybrid.dropPg(pg)
	require.Nil(t, hybrid.currentPg())
}

// TestHybridPGWriteRetrySurvivesBlip verifies a transient Postgres failure is
// retried within the budget and succeeds without dropping to SQLite.
func TestHybridPGWriteRetrySurvivesBlip(t *testing.T) {
	prev := gatewayPGWriteRetryBaseBackoff
	gatewayPGWriteRetryBaseBackoff = time.Millisecond
	t.Cleanup(func() { gatewayPGWriteRetryBaseBackoff = prev })

	hybrid := NewHybridGatewayStore(nil, nil, timeHour, gatewayPGConnectTimeout)
	hybrid.writeRetryBudget = time.Second

	pg := &PostgresGatewayStore{}
	attempts := 0
	err := hybrid.attemptPGWrite(context.Background(), pg, func(*PostgresGatewayStore) error {
		attempts++
		if attempts < 3 {
			return fmt.Errorf("blip")
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, attempts)
}

// TestHybridPGWriteRetryBudgetDisabled verifies a zero budget makes a single
// attempt with no retry.
func TestHybridPGWriteRetryBudgetDisabled(t *testing.T) {
	hybrid := NewHybridGatewayStore(nil, nil, timeHour, gatewayPGConnectTimeout)
	hybrid.writeRetryBudget = 0

	pg := &PostgresGatewayStore{}
	attempts := 0
	err := hybrid.attemptPGWrite(context.Background(), pg, func(*PostgresGatewayStore) error {
		attempts++
		return fmt.Errorf("down")
	})
	require.Error(t, err)
	require.Equal(t, 1, attempts)
}

// TestHybridWriteFallsBackAfterRetryBudget verifies that once the retry budget
// is exhausted the write drops Postgres, routes to SQLite, and journals.
func TestHybridWriteFallsBackAfterRetryBudget(t *testing.T) {
	sqlite := newTestSQLiteGatewayStoreOnly(t)
	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   1,
		MaxInputTokensInFlight:  100,
	}.WithTuningDefaults()
	require.NoError(t, sqlite.Initialize(settings, nil))

	hybrid := NewHybridGatewayStore(nil, sqlite, timeHour, gatewayPGConnectTimeout)
	hybrid.writeRetryBudget = 0
	t.Cleanup(func() { require.NoError(t, hybrid.Close()) })

	pgAttempts := 0
	failingPG := &PostgresGatewayStore{}
	hybrid.mu.Lock()
	hybrid.pg = failingPG
	hybrid.mu.Unlock()

	err := hybrid.write(context.Background(),
		func(*PostgresGatewayStore) error {
			pgAttempts++
			return fmt.Errorf("pg down")
		},
		func(sqlite *SQLiteGatewayStore) error { return sqlite.UpdateSettings(settings) },
		[]gatewaySyncEntry{{tableName: gatewayTableSettings, rowKey: gatewaySettingsRowKey, op: gatewaySyncOpUpsert}},
		func(tx *sql.Tx) error { return sqlite.updateSettingsTx(tx, settings) },
	)
	require.NoError(t, err)
	require.Equal(t, 1, pgAttempts)
	require.Nil(t, hybrid.currentPg())

	count, err := sqlite.countSyncJournalEntries()
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

const timeHour = time.Hour
