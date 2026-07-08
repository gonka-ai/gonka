package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func seedGatewayStoreForMigration(t *testing.T, store GatewayStore) GatewayState {
	t.Helper()

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1500,
		MaxConcurrentRequests:   4,
		MaxInputTokensInFlight:  400,
		Disabled: GatewayDisabledSettings{
			Enabled: true,
			Message: "migrating",
			NewURL:  "https://example.com/v1",
		},
		EscrowRotation: EscrowRotationSettings{
			Enabled:           true,
			SettlementEnabled: true,
			PrePoCBlocks:      99,
			Models: []EscrowRotationModelSettings{{
				ModelID:       "Kimi/Rotate",
				TempCount:     1,
				TargetCount:   4,
				Amount:        500,
				PrivateKeyEnv: "ROTATION_KEY",
			}},
		},
	}.WithTuningDefaults()

	devshards := []GatewayDevshardState{
		{
			RuntimeConfig: RuntimeConfig{
				ID:            "ds-1",
				PrivateKeyHex: "aaa",
				Model:         "Qwen/Test",
				StoragePath:   "/data/ds-1",
			},
			Active:            true,
			RotationRole:      rotationRoleRegular,
			RotationEpoch:     5,
			SettlementPending: false,
			CreatedAt:         "2026-01-01T00:00:00Z",
			UpdatedAt:         "2026-01-02T00:00:00Z",
		},
		{
			RuntimeConfig: RuntimeConfig{
				ID:            "ds-2",
				PrivateKeyHex: "bbb",
				Model:         "Kimi/Rotate",
				StoragePath:   "/data/ds-2",
			},
			Active:            false,
			RotationRole:      rotationRoleTemp,
			RotationEpoch:     6,
			SettlementPending: true,
			CreatedAt:         "2026-01-03T00:00:00Z",
			UpdatedAt:         "2026-01-04T00:00:00Z",
		},
	}

	require.NoError(t, store.Initialize(settings, devshards))
	_, err := store.UpsertSuspiciousHosts([]string{"host-a", "host-b"}, "suspicious")
	require.NoError(t, err)
	require.NoError(t, store.SaveRotationStatus(GatewayRotationStatus{
		ModelID:       "Qwen/Test",
		Stage:         "prepare_temp",
		Epoch:         10,
		Role:          rotationRoleTemp,
		TargetCount:   4,
		ExistingCount: 1,
		CreatedCount:  2,
		Completed:     true,
		UpdatedAt:     "2026-05-04T00:00:00Z",
	}))
	require.NoError(t, store.SaveCommitment(GatewayEscrowCommitment{
		TxHash:        "TX-MIGRATE-1",
		Model:         "Qwen/Test",
		Role:          rotationRoleTemp,
		Epoch:         10,
		PrivateKeyEnv: "KEY_ENV",
		BlockHeight:   12345,
		CreatedAt:     time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC),
	}))
	require.NoError(t, store.SaveCommitment(GatewayEscrowCommitment{
		TxHash:        "TX-MIGRATE-2",
		Model:         "Kimi/Rotate",
		Role:          rotationRoleRegular,
		Epoch:         11,
		PrivateKeyEnv: "KEY_ENV_2",
		BlockHeight:   12346,
		CreatedAt:     time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC),
	}))
	quarantine := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.SaveParticipantThrottle(
		"participant-1",
		[]string{"Qwen/Test", "Kimi/Rotate"},
		12.5,
		time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC),
		503,
		quarantine,
		2,
	))
	require.NoError(t, store.SaveParticipantThrottle(
		"participant-2",
		nil,
		0,
		time.Date(2026, 5, 7, 11, 0, 0, 0, time.UTC),
		0,
		time.Time{},
		0,
	))

	state, has, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, has)
	return state
}

func assertGatewayStoresEqual(t *testing.T, want GatewayStore, got GatewayStore) {
	t.Helper()

	wantState, wantHas, err := want.LoadState()
	require.NoError(t, err)
	require.True(t, wantHas)

	gotState, gotHas, err := got.LoadState()
	require.NoError(t, err)
	require.True(t, gotHas)
	require.Equal(t, wantState.Settings, gotState.Settings)
	require.ElementsMatch(t, wantState.Devshards, gotState.Devshards)
	require.ElementsMatch(t, wantState.SuspiciousHosts, gotState.SuspiciousHosts)

	wantStatuses, err := want.LoadRotationStatuses(0)
	require.NoError(t, err)
	gotStatuses, err := got.LoadRotationStatuses(0)
	require.NoError(t, err)
	require.ElementsMatch(t, wantStatuses, gotStatuses)

	wantCommitments, err := want.LoadCommitments()
	require.NoError(t, err)
	gotCommitments, err := got.LoadCommitments()
	require.NoError(t, err)
	require.ElementsMatch(t, wantCommitments, gotCommitments)

	wantThrottles, err := want.LoadParticipantThrottles()
	require.NoError(t, err)
	gotThrottles, err := got.LoadParticipantThrottles()
	require.NoError(t, err)
	require.ElementsMatch(t, wantThrottles, gotThrottles)
}

func TestMigrateGatewaySQLiteToPostgres_FullFidelity(t *testing.T) {
	ctx := context.Background()

	sqlite, err := NewSQLiteGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlite.Close()) })
	seedGatewayStoreForMigration(t, sqlite)

	pg := newTestPostgresGatewayStore(t)

	require.NoError(t, MigrateGatewaySQLiteToPostgres(ctx, sqlite, pg))
	assertGatewayStoresEqual(t, sqlite, pg)

	commitments, err := pg.LoadCommitments()
	require.NoError(t, err)
	require.Len(t, commitments, 2)
	require.ElementsMatch(t, []string{"TX-MIGRATE-1", "TX-MIGRATE-2"}, []string{commitments[0].TxHash, commitments[1].TxHash})
}

func TestMigrateGatewaySQLiteToPostgres_Idempotent(t *testing.T) {
	ctx := context.Background()

	sqlite, err := NewSQLiteGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlite.Close()) })
	seedGatewayStoreForMigration(t, sqlite)

	pg := newTestPostgresGatewayStore(t)

	require.NoError(t, MigrateGatewaySQLiteToPostgres(ctx, sqlite, pg))
	firstState, _, err := pg.LoadState()
	require.NoError(t, err)

	require.NoError(t, MigrateGatewaySQLiteToPostgres(ctx, sqlite, pg))
	secondState, _, err := pg.LoadState()
	require.NoError(t, err)
	require.Equal(t, firstState, secondState)

	migrated, err := pg.hasMigrationMarker(ctx, gatewaySQLiteImportMarker)
	require.NoError(t, err)
	require.True(t, migrated)
}

func TestMigrateGatewaySQLiteToPostgres_EmptinessGuard(t *testing.T) {
	ctx := context.Background()

	sqlite, err := NewSQLiteGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlite.Close()) })
	seedGatewayStoreForMigration(t, sqlite)

	pg := newTestPostgresGatewayStore(t)
	existing := GatewaySettings{
		ChainREST:               "http://existing:1317",
		PublicAPI:               "http://existing:9000",
		DefaultModel:            "Existing/Model",
		DefaultRequestMaxTokens: 999,
		MaxConcurrentRequests:   1,
		MaxInputTokensInFlight:  50,
	}.WithTuningDefaults()
	require.NoError(t, pg.Initialize(existing, nil))

	require.NoError(t, MigrateGatewaySQLiteToPostgres(ctx, sqlite, pg))

	state, has, err := pg.LoadState()
	require.NoError(t, err)
	require.True(t, has)
	require.Equal(t, existing, state.Settings)
}

func TestMigrateGatewaySQLiteToPostgres_EmptySource(t *testing.T) {
	ctx := context.Background()

	sqlite, err := NewSQLiteGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlite.Close()) })

	pg := newTestPostgresGatewayStore(t)

	require.NoError(t, MigrateGatewaySQLiteToPostgres(ctx, sqlite, pg))

	_, has, err := pg.LoadState()
	require.NoError(t, err)
	require.False(t, has)

	migrated, err := pg.hasMigrationMarker(ctx, gatewaySQLiteImportMarker)
	require.NoError(t, err)
	require.False(t, migrated)
}

func TestMigrateGatewaySQLiteToPostgres_RollbackLeavesNoPartialRows(t *testing.T) {
	ctx := context.Background()

	sqlite, err := NewSQLiteGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlite.Close()) })
	seedGatewayStoreForMigration(t, sqlite)

	pg := newTestPostgresGatewayStore(t)

	srcState, _, err := sqlite.LoadState()
	require.NoError(t, err)

	tx, err := pg.pool.Begin(ctx)
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	insertArgs := settingsInsertArgs(srcState.Settings, now)
	_, err = tx.Exec(ctx, `
		INSERT INTO gateway_settings (`+gatewaySettingsInsertColumnNames()+`)
		VALUES (`+pgPlaceholderList(len(insertArgs))+`)`, insertArgs...)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))

	_, has, err := pg.LoadState()
	require.NoError(t, err)
	require.False(t, has)

	require.NoError(t, MigrateGatewaySQLiteToPostgres(ctx, sqlite, pg))
	assertGatewayStoresEqual(t, sqlite, pg)
}

func TestMigrateGatewaySQLiteToPostgres_CommitmentsPreserved(t *testing.T) {
	ctx := context.Background()

	sqlite, err := NewSQLiteGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlite.Close()) })

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   1,
		MaxInputTokensInFlight:  100,
	}.WithTuningDefaults()
	require.NoError(t, sqlite.Initialize(settings, nil))

	seed := []GatewayEscrowCommitment{
		{
			TxHash:        "TX-AAA",
			Model:         "Qwen/Test",
			Role:          rotationRoleTemp,
			Epoch:         7,
			PrivateKeyEnv: "ENV_A",
			BlockHeight:   100,
			CreatedAt:     time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			TxHash:        "TX-BBB",
			Model:         "Kimi/Rotate",
			Role:          rotationRoleRegular,
			Epoch:         8,
			PrivateKeyEnv: "ENV_B",
			BlockHeight:   200,
			CreatedAt:     time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, c := range seed {
		require.NoError(t, sqlite.SaveCommitment(c))
	}

	pg := newTestPostgresGatewayStore(t)
	require.NoError(t, MigrateGatewaySQLiteToPostgres(ctx, sqlite, pg))

	want, err := sqlite.LoadCommitments()
	require.NoError(t, err)
	got, err := pg.LoadCommitments()
	require.NoError(t, err)
	require.ElementsMatch(t, want, got)
	require.Len(t, got, 2)
	require.ElementsMatch(t, []string{"TX-AAA", "TX-BBB"}, []string{got[0].TxHash, got[1].TxHash})
}
