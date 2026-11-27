package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

func boot(ctx context.Context, pool *sql.DB) error {
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning the Postgres boot: %w", err)
	}
	defer tx.Rollback()
	for _, statement := range []string{`SET LOCAL statement_timeout = 0`, `SET LOCAL lock_timeout = 0`} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("lifting the Postgres timeouts for the boot: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1::text || current_schema()))`, bootLockName); err != nil {
		return fmt.Errorf("locking the Postgres boot: %w", err)
	}
	if err := requireDurableCommits(ctx, tx); err != nil {
		return err
	}
	if err := claimDatabase(ctx, tx); err != nil {
		return err
	}
	found, err := migrate(ctx, tx)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing the Postgres boot: %w", err)
	}
	logNewerSchema(found, len(migrations))
	return nil
}

func requireDurableCommits(ctx context.Context, tx *sql.Tx) error {
	for _, setting := range []string{synchronousCommitParam, fsyncSetting} {
		var value string
		if err := tx.QueryRowContext(ctx, `SELECT current_setting($1)`, setting).Scan(&value); err != nil {
			return fmt.Errorf("reading the Postgres setting %s: %w", setting, err)
		}
		if value == settingOff {
			return fmt.Errorf("%w: %s is off", ErrDurabilityOff, setting)
		}
	}
	return nil
}

func claimDatabase(ctx context.Context, tx *sql.Tx) error {
	var marked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_tables WHERE schemaname = current_schema() AND tablename = $1::text)`, markerTable).Scan(&marked); err != nil {
		return fmt.Errorf("looking for the gateway marker table: %w", err)
	}
	if marked {
		return nil
	}
	var tables int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname = current_schema()`).Scan(&tables); err != nil {
		return fmt.Errorf("counting tables of the Postgres database: %w", err)
	}
	if tables > 0 {
		return ErrForeignDatabase
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE `+markerTable+` (
		id BIGINT PRIMARY KEY CHECK (id = 1),
		created_at TEXT NOT NULL DEFAULT (`+nowExpression+`)
	)`); err != nil {
		return fmt.Errorf("creating the gateway marker table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+markerTable+` (id) VALUES (1)`); err != nil {
		return fmt.Errorf("writing the gateway marker: %w", err)
	}
	return nil
}

func migrate(ctx context.Context, tx *sql.Tx) (int, error) {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version BIGINT NOT NULL)`); err != nil {
		return 0, fmt.Errorf("creating schema_version: %w", err)
	}
	var currentVersion int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&currentVersion); err != nil {
		return 0, fmt.Errorf("reading schema version: %w", err)
	}
	for index := currentVersion; index < len(migrations); index++ {
		if _, err := tx.ExecContext(ctx, migrations[index]); err != nil {
			return 0, fmt.Errorf("applying migration %d: %w", index+1, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES ($1)`, index+1); err != nil {
			return 0, fmt.Errorf("recording migration %d: %w", index+1, err)
		}
	}
	return currentVersion, nil
}
