package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"
)

const defaultGatewayPGConnectTimeout = 2 * time.Second

// NewGatewayStore opens the gateway persistence layer based on environment configuration.
// If PGHOST is unset, returns SQLiteGatewayStore only.
// If PGHOST is set, runs one-shot migration from SQLite when Postgres is reachable,
// then returns HybridGatewayStore (Postgres primary + SQLite fallback).
func NewGatewayStore(ctx context.Context, baseStorageDir string) (GatewayStore, error) {
	sqlite, err := NewSQLiteGatewayStore(filepath.Join(baseStorageDir, "gateway.db"))
	if err != nil {
		return nil, err
	}

	pgHost := os.Getenv("PGHOST")
	if pgHost == "" {
		return sqlite, nil
	}

	retryInterval, err := time.ParseDuration(os.Getenv("PG_RETRY_INTERVAL"))
	if err != nil || retryInterval <= 0 {
		retryInterval = defaultGatewayPGRetryInterval
	}
	connectTimeout, err := time.ParseDuration(os.Getenv("PG_CONNECT_TIMEOUT"))
	if err != nil || connectTimeout <= 0 {
		connectTimeout = defaultGatewayPGConnectTimeout
	}

	pg, pgErr := NewPostgresGatewayStore(ctx)
	if pgErr != nil {
		log.Printf("gateway store: postgres connection failed, will retry lazily on write (host=%s): %v", pgHost, pgErr)
		return NewHybridGatewayStore(nil, sqlite, retryInterval, connectTimeout), nil
	}

	if err := MigrateGatewaySQLiteToPostgres(ctx, sqlite, pg); err != nil {
		_ = pg.Close()
		_ = sqlite.Close()
		return nil, err
	}

	hybrid := NewHybridGatewayStore(nil, sqlite, retryInterval, connectTimeout)
	if err := hybrid.ReconcileSyncJournal(ctx, pg); err != nil {
		_ = pg.Close()
		log.Printf("gateway store: sync journal drain failed at startup, staying on sqlite fallback: %v", err)
		return hybrid, nil
	}

	log.Printf("gateway store: using postgres with sqlite fallback (host=%s)", pgHost)
	return hybrid, nil
}
