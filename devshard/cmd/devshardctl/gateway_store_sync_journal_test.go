package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"common/storage/mode"

	"github.com/stretchr/testify/require"
)

// seedLegacySyncJournalRow appends a journal row the way a pre-Postgres-only
// build did when a write fell back to SQLite. Nothing writes the journal
// anymore, so the drain is exercised against rows left on disk by an older
// deployment: the caller writes the data row through the normal SQLite API and
// records the matching journal entry here.
func seedLegacySyncJournalRow(t *testing.T, sqlite *SQLiteGatewayStore, tableName, rowKey, op string) {
	t.Helper()
	_, err := sqlite.db.Exec(`
		INSERT INTO gateway_pg_sync_journal (table_name, row_key, op, enqueued_at)
		VALUES (?, ?, ?, ?)`,
		tableName, rowKey, op, time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
}

func baseSyncJournalSettings() GatewaySettings {
	return GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   1,
		MaxInputTokensInFlight:  100,
	}.WithTuningDefaults()
}

func TestSyncJournalDrainNamesBadRowInError(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	pg, err := NewPostgresGatewayStore(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Close()) })

	sqlite := newTestSQLiteGatewayStoreOnly(t)
	require.NoError(t, sqlite.Initialize(ctx, baseSyncJournalSettings(), nil))
	seedLegacySyncJournalRow(t, sqlite, "not_a_gateway_table", "bad-key", gatewaySyncOpUpsert)

	err = drainGatewaySyncJournalUntilEmpty(ctx, sqlite, pg)
	require.Error(t, err)
	require.Contains(t, err.Error(), `table="not_a_gateway_table"`)
	require.Contains(t, err.Error(), `row_key="bad-key"`)
	require.Contains(t, err.Error(), `op="upsert"`)
}

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

func TestSyncJournalRowShapePersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	sqlite, err := NewSQLiteGatewayStore(path)
	require.NoError(t, err)
	require.NoError(t, sqlite.Initialize(context.Background(), baseSyncJournalSettings(), nil))
	seedLegacySyncJournalRow(t, sqlite, gatewayTableSettings, gatewaySettingsRowKey, gatewaySyncOpUpsert)
	require.NoError(t, sqlite.Close())

	reopened, err := NewSQLiteGatewayStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	count, err := reopened.countSyncJournalEntries()
	require.NoError(t, err)
	require.Equal(t, 1, count)

	rows, err := reopened.loadSyncJournalChunk(0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, gatewayTableSettings, rows[0].tableName)
	require.Equal(t, gatewaySettingsRowKey, rows[0].rowKey)
	require.Equal(t, gatewaySyncOpUpsert, rows[0].op)
}

func TestSyncJournalDrainProcessesInChunks(t *testing.T) {
	prev := gatewaySyncJournalDrainChunkSize
	gatewaySyncJournalDrainChunkSize = 2
	t.Cleanup(func() { gatewaySyncJournalDrainChunkSize = prev })

	cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	pg, err := NewPostgresGatewayStore(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Close()) })

	sqlite := newTestSQLiteGatewayStoreOnly(t)
	base := baseSyncJournalSettings()
	require.NoError(t, pg.Initialize(context.Background(), base, nil))
	require.NoError(t, sqlite.Initialize(context.Background(), base, nil))

	// Five journaled updates to the same row: the drain must span three chunks
	// and replay the row's final value once per chunk.
	for i := 0; i < 5; i++ {
		updated := base
		updated.DefaultRequestMaxTokens = uint64(2000 + i)
		require.NoError(t, sqlite.UpdateSettings(context.Background(), updated))
		seedLegacySyncJournalRow(t, sqlite, gatewayTableSettings, gatewaySettingsRowKey, gatewaySyncOpUpsert)
	}

	count, err := sqlite.countSyncJournalEntries()
	require.NoError(t, err)
	require.Equal(t, 5, count)

	require.NoError(t, drainGatewaySyncJournalUntilEmpty(ctx, sqlite, pg))

	pgState, has, err := pg.LoadState(context.Background())
	require.NoError(t, err)
	require.True(t, has)
	require.EqualValues(t, 2004, pgState.Settings.DefaultRequestMaxTokens)

	count, err = sqlite.countSyncJournalEntries()
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestSyncJournalDrainUpsertDeleteAcrossChunks(t *testing.T) {
	prev := gatewaySyncJournalDrainChunkSize
	gatewaySyncJournalDrainChunkSize = 1
	t.Cleanup(func() { gatewaySyncJournalDrainChunkSize = prev })

	cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	pg, err := NewPostgresGatewayStore(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Close()) })

	sqlite := newTestSQLiteGatewayStoreOnly(t)
	base := baseSyncJournalSettings()
	require.NoError(t, pg.Initialize(context.Background(), base, nil))
	require.NoError(t, sqlite.Initialize(context.Background(), base, nil))

	// Seed PG with a commitment that the outage-era save+delete should remove.
	require.NoError(t, pg.SaveCommitment(context.Background(), GatewayEscrowCommitment{
		TxHash: "TX-CHUNK-1", Model: "Qwen/Test", Role: rotationRoleTemp, Epoch: 7,
	}))

	require.NoError(t, sqlite.SaveCommitment(context.Background(), GatewayEscrowCommitment{
		TxHash: "TX-CHUNK-1", Model: "Qwen/Test", Role: rotationRoleTemp, Epoch: 7,
	}))
	seedLegacySyncJournalRow(t, sqlite, gatewayTableCommitments, "TX-CHUNK-1", gatewaySyncOpUpsert)
	require.NoError(t, sqlite.DeleteCommitment(context.Background(), "TX-CHUNK-1"))
	seedLegacySyncJournalRow(t, sqlite, gatewayTableCommitments, "TX-CHUNK-1", gatewaySyncOpDelete)

	count, err := sqlite.countSyncJournalEntries()
	require.NoError(t, err)
	require.Equal(t, 2, count)

	require.NoError(t, drainGatewaySyncJournalUntilEmpty(ctx, sqlite, pg))

	commitments, err := pg.LoadCommitments(context.Background())
	require.NoError(t, err)
	require.Empty(t, commitments)

	count, err = sqlite.countSyncJournalEntries()
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestSyncJournalDrainRestoresOutageDeltas(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	pg, err := NewPostgresGatewayStore(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Close()) })

	sqlite := newTestSQLiteGatewayStoreOnly(t)
	base := baseSyncJournalSettings()
	require.NoError(t, pg.Initialize(context.Background(), base, []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{ID: "keep-me", Model: "Qwen/Test", StoragePath: "/data/keep"},
		Active:        true,
	}}))
	require.NoError(t, sqlite.Initialize(context.Background(), base, []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{ID: "keep-me", Model: "Qwen/Test", StoragePath: "/data/keep"},
		Active:        true,
	}, {
		RuntimeConfig: RuntimeConfig{ID: "outage-only", Model: "Kimi/Rotate", StoragePath: "/data/outage"},
		Active:        true,
	}}))

	outageSettings := base
	outageSettings.DefaultRequestMaxTokens = 5555
	require.NoError(t, sqlite.UpdateSettings(context.Background(), outageSettings))
	seedLegacySyncJournalRow(t, sqlite, gatewayTableSettings, gatewaySettingsRowKey, gatewaySyncOpUpsert)
	require.NoError(t, sqlite.UpsertDevshard(context.Background(), GatewayDevshardState{
		RuntimeConfig: RuntimeConfig{ID: "outage-only", Model: "Kimi/Rotate", StoragePath: "/data/outage-new"},
		Active:        true,
	}))
	seedLegacySyncJournalRow(t, sqlite, gatewayTableDevshards, "outage-only", gatewaySyncOpUpsert)
	require.NoError(t, sqlite.DeleteDevshard(context.Background(), "keep-me"))
	seedLegacySyncJournalRow(t, sqlite, gatewayTableDevshards, "keep-me", gatewaySyncOpDelete)

	require.NoError(t, drainGatewaySyncJournalUntilEmpty(ctx, sqlite, pg))

	pgState, has, err := pg.LoadState(context.Background())
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
	require.NoError(t, pg.Initialize(context.Background(), pgOnly, nil))

	staleSQLite := GatewaySettings{
		ChainREST:               "http://stale:1317",
		PublicAPI:               "http://stale:9000",
		DefaultModel:            "Stale/Model",
		DefaultRequestMaxTokens: 1,
		MaxConcurrentRequests:   1,
		MaxInputTokensInFlight:  1,
	}.WithTuningDefaults()
	require.NoError(t, sqlite.Initialize(context.Background(), staleSQLite, nil))

	// Only the rotation status was journaled, so the stale SQLite settings row
	// must not travel with it.
	status := GatewayRotationStatus{ModelID: "Qwen/Test", Stage: "prepare_temp", Epoch: 3, Completed: true}
	require.NoError(t, sqlite.SaveRotationStatus(context.Background(), status))
	seedLegacySyncJournalRow(t, sqlite, gatewayTableRotationStatus,
		gatewayRotationStatusRowKey(status.ModelID, status.Stage, status.Epoch), gatewaySyncOpUpsert)

	require.NoError(t, drainGatewaySyncJournalUntilEmpty(ctx, sqlite, pg))

	pgState, has, err := pg.LoadState(context.Background())
	require.NoError(t, err)
	require.True(t, has)
	require.Equal(t, pgOnly, pgState.Settings)

	statuses, err := pg.LoadRotationStatuses(context.Background(), 0)
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
	settings := baseSyncJournalSettings()
	require.NoError(t, sqlite.Initialize(context.Background(), settings, nil))
	seedLegacySyncJournalRow(t, sqlite, gatewayTableSettings, gatewaySettingsRowKey, gatewaySyncOpUpsert)

	require.NoError(t, drainGatewaySyncJournalUntilEmpty(ctx, sqlite, pg))
	require.NoError(t, drainGatewaySyncJournalUntilEmpty(ctx, sqlite, pg))

	count, err := sqlite.countSyncJournalEntries()
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

// TestNewGatewayStoreDrainsLegacySyncJournal covers the production path: a
// gateway upgraded from a hybrid build boots with unreplayed journal rows in
// gateway.db and must publish them to Postgres before serving.
func TestNewGatewayStoreDrainsLegacySyncJournal(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)
	t.Setenv(mode.EnvStorageMode, "postgres")

	ctx := context.Background()
	base := baseSyncJournalSettings()

	// Postgres already holds gateway settings, so the bulk migration is skipped
	// and only the journal drain can carry the outage-era update across.
	seed, err := NewPostgresGatewayStore(ctx)
	require.NoError(t, err)
	require.NoError(t, seed.Initialize(context.Background(), base, nil))
	require.NoError(t, seed.Close())

	baseDir := t.TempDir()
	sqlitePath := filepath.Join(baseDir, "gateway.db")
	sqlite, err := NewSQLiteGatewayStore(sqlitePath)
	require.NoError(t, err)
	require.NoError(t, sqlite.Initialize(context.Background(), base, nil))
	journaled := base
	journaled.DefaultRequestMaxTokens = 3300
	require.NoError(t, sqlite.UpdateSettings(context.Background(), journaled))
	seedLegacySyncJournalRow(t, sqlite, gatewayTableSettings, gatewaySettingsRowKey, gatewaySyncOpUpsert)
	require.NoError(t, sqlite.Close())

	store, err := NewGatewayStore(ctx, baseDir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.IsType(t, &PostgresGatewayStore{}, store)

	pgState, has, err := store.LoadState(context.Background())
	require.NoError(t, err)
	require.True(t, has)
	require.EqualValues(t, 3300, pgState.Settings.DefaultRequestMaxTokens)

	reopened, err := NewSQLiteGatewayStore(sqlitePath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	count, err := reopened.countSyncJournalEntries()
	require.NoError(t, err)
	require.Equal(t, 0, count, "boot must drain the legacy journal")
}
