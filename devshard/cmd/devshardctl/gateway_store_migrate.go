package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const gatewaySQLiteImportMarker = "sqlite_import"

type gatewayMigrationExecer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// MigrateGatewaySQLiteToPostgres copies existing SQLite gateway data into Postgres
// once. Idempotency is the migration marker — not "Postgres already has a
// settings row". A marker is written only after a successful import, when the
// destination already holds gateway state (so we must not clobber it), or when
// the source is empty (nothing to import). A crash mid-import rolls the
// transaction back and leaves no marker, so the next boot retries.
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

	_, hasDst, err := dst.LoadState(ctx)
	if err != nil {
		return fmt.Errorf("check destination gateway settings: %w", err)
	}
	if hasDst {
		// Postgres already owns gateway state (another instance, a prior hybrid
		// write, or Initialize). Importing would clobber it — claim the
		// migration done so every subsequent boot does not re-evaluate the
		// local sqlite file as a pending import.
		log.Printf("gateway store: skipping sqlite import, postgres already holds gateway settings; writing %s marker and leaving local sqlite state unused", gatewaySQLiteImportMarker)
		return dst.writeMigrationMarker(ctx, gatewaySQLiteImportMarker)
	}

	srcState, hasSrc, err := src.LoadState(ctx)
	if err != nil {
		return fmt.Errorf("load source gateway state: %w", err)
	}
	if !hasSrc {
		return dst.writeMigrationMarker(ctx, gatewaySQLiteImportMarker)
	}

	rotationStatuses, err := src.LoadRotationStatuses(ctx, 0)
	if err != nil {
		return fmt.Errorf("load source rotation statuses: %w", err)
	}
	commitments, err := src.LoadCommitments(ctx)
	if err != nil {
		return fmt.Errorf("load source commitments: %w", err)
	}
	throttles, err := src.LoadParticipantThrottles(ctx)
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

	if err := writeGatewayMigrationMarker(ctx, tx, gatewaySQLiteImportMarker, now); err != nil {
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

func (s *PostgresGatewayStore) writeMigrationMarker(ctx context.Context, name string) error {
	if err := s.ensureMigrationTable(ctx); err != nil {
		return err
	}
	return writeGatewayMigrationMarker(ctx, s.pool, name, time.Now().UTC().Format(time.RFC3339Nano))
}

func writeGatewayMigrationMarker(ctx context.Context, exec gatewayMigrationExecer, name, completedAt string) error {
	_, err := exec.Exec(ctx, `
		INSERT INTO gateway_migration (name, completed_at)
		VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET completed_at = EXCLUDED.completed_at`,
		name, completedAt,
	)
	return err
}
