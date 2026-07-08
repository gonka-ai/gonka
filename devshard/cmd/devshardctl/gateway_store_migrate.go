package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const gatewaySQLiteImportMarker = "sqlite_import"

// MigrateGatewaySQLiteToPostgres copies existing SQLite gateway data into Postgres
// before hybrid serving. It is idempotent: a marker row prevents re-import.
func MigrateGatewaySQLiteToPostgres(ctx context.Context, src GatewayStore, dst *PostgresGatewayStore) error {
	if src == nil || dst == nil || dst.pool == nil {
		return nil
	}

	if err := dst.ensureMigrationTable(ctx); err != nil {
		return err
	}
	migrated, err := dst.hasMigrationMarker(ctx, gatewaySQLiteImportMarker)
	if err != nil {
		return err
	}
	if migrated {
		return nil
	}

	_, hasDst, err := dst.LoadState()
	if err != nil {
		return fmt.Errorf("check destination gateway settings: %w", err)
	}
	if hasDst {
		return nil
	}

	srcState, hasSrc, err := src.LoadState()
	if err != nil {
		return fmt.Errorf("load source gateway state: %w", err)
	}
	if !hasSrc {
		return nil
	}

	rotationStatuses, err := src.LoadRotationStatuses(0)
	if err != nil {
		return fmt.Errorf("load source rotation statuses: %w", err)
	}
	commitments, err := src.LoadCommitments()
	if err != nil {
		return fmt.Errorf("load source commitments: %w", err)
	}
	throttles, err := src.LoadParticipantThrottles()
	if err != nil {
		return fmt.Errorf("load source participant throttles: %w", err)
	}

	tx, err := dst.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin gateway migration: %w", err)
	}
	defer tx.Rollback(ctx)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := applyGatewaySettingsUpsertToPG(ctx, tx, srcState.Settings, now); err != nil {
		return fmt.Errorf("migrate gateway settings: %w", err)
	}

	for _, devshard := range srcState.Devshards {
		if err := applyGatewayDevshardToPG(ctx, tx, devshard, now); err != nil {
			return err
		}
	}
	for _, host := range srcState.SuspiciousHosts {
		if err := applyGatewaySuspiciousHostToPG(ctx, tx, host); err != nil {
			return fmt.Errorf("migrate suspicious host %s: %w", host.ParticipantKey, err)
		}
	}
	for _, status := range rotationStatuses {
		if err := applyGatewayRotationStatusToPG(ctx, tx, status); err != nil {
			return fmt.Errorf("migrate rotation status model=%q stage=%q epoch=%d: %w", status.ModelID, status.Stage, status.Epoch, err)
		}
	}
	for _, c := range commitments {
		if err := applyGatewayCommitmentToPG(ctx, tx, c); err != nil {
			return fmt.Errorf("migrate escrow commitment tx=%s: %w", c.TxHash, err)
		}
	}
	for _, row := range throttles {
		if err := applyGatewayThrottleToPG(ctx, tx, row); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO gateway_migration (name, completed_at)
		VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET completed_at = EXCLUDED.completed_at`,
		gatewaySQLiteImportMarker, now,
	); err != nil {
		return fmt.Errorf("write gateway migration marker: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit gateway migration: %w", err)
	}
	return nil
}

func (s *PostgresGatewayStore) ensureMigrationTable(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS gateway_migration (
			name TEXT PRIMARY KEY,
			completed_at TEXT NOT NULL
		)`)
	return err
}

func (s *PostgresGatewayStore) hasMigrationMarker(ctx context.Context, name string) (bool, error) {
	var completedAt string
	err := s.pool.QueryRow(ctx, `SELECT completed_at FROM gateway_migration WHERE name = $1`, name).Scan(&completedAt)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
