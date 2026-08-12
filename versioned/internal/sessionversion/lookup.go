package sessionversion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Lookup resolves escrow → bound protocol version from shared Postgres session
// metadata (devshard_session_index + devshard_sessions).
type Lookup struct {
	pool *pgxpool.Pool
}

// OpenFromEnv connects using libpq env (PGHOST/PGUSER/… or DATABASE_URL).
// Returns (nil, nil) when Postgres is not configured or explicitly disabled.
func OpenFromEnv(ctx context.Context, ha bool) (*Lookup, error) {
	if os.Getenv("VERSIOND_DISABLE_SESSION_LOOKUP") == "true" {
		slog.Info("session version lookup disabled by VERSIOND_DISABLE_SESSION_LOOKUP")
		return nil, nil
	}
	if !postgresConfigured() {
		slog.Info("session version lookup disabled (no PGHOST/DATABASE_URL); versionless obs uses fan-out")
		return nil, nil
	}

	connString := os.Getenv("DATABASE_URL")
	if connString != "" && pgEnvironmentConfigured() {
		return nil, errors.New("DATABASE_URL cannot be combined with PG* connection variables; use one PostgreSQL configuration")
	}
	if connString != "" && ha {
		return nil, errors.New("DATABASE_URL is unsupported in HA; use PGHOST/PGPORT/PGDATABASE/PGUSER/PGPASSWORD so versiond and its children share one database")
	}
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}
	cfg.MaxConns = 4
	cfg.MinConns = 0
	cfg.MaxConnLifetime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	slog.Info("session version lookup enabled (postgres)")
	return &Lookup{pool: pool}, nil
}

func postgresConfigured() bool {
	if os.Getenv("DATABASE_URL") != "" {
		return true
	}
	return pgEnvironmentConfigured()
}

func pgEnvironmentConfigured() bool {
	for _, name := range []string{
		"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD",
		"PGPASSFILE", "PGSERVICE", "PGSERVICEFILE", "PGSSLMODE",
		"PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGSSLPASSWORD",
		"PGAPPNAME", "PGCONNECT_TIMEOUT", "PGTARGETSESSIONATTRS",
	} {
		if os.Getenv(name) != "" {
			return true
		}
	}
	return false
}

// Close releases the pool.
func (l *Lookup) Close() {
	if l != nil && l.pool != nil {
		l.pool.Close()
	}
}

// LookupSessionVersion implements proxy.SessionVersionLookup.
func (l *Lookup) LookupSessionVersion(ctx context.Context, escrowID string) (string, bool, error) {
	if l == nil || l.pool == nil {
		return "", false, nil
	}
	if escrowID == "" {
		return "", false, nil
	}
	qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var version *string
	err := l.pool.QueryRow(qctx, `
		SELECT s.version
		FROM devshard_session_index i
		JOIN devshard_sessions s
		  ON s.epoch_id = i.epoch_id AND s.escrow_id = i.escrow_id
		WHERE i.escrow_id = $1
		  AND s.status = 'active'
		LIMIT 1`, escrowID).Scan(&version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	if version == nil || *version == "" {
		return "", false, nil
	}
	return *version, true, nil
}

// StorageIdentity returns the durable identity created by the devshard schema.
// It lets deployment tooling prove that independently configured supervisors
// resolve to the same database, rather than merely comparing libpq settings.
func (l *Lookup) StorageIdentity(ctx context.Context) (string, error) {
	if l == nil || l.pool == nil {
		return "", errors.New("postgres session lookup is unavailable")
	}
	qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var identity string
	if err := l.pool.QueryRow(qctx, `
		SELECT identity::text
		FROM devshard_storage_identity
		WHERE singleton`).Scan(&identity); err != nil {
		return "", fmt.Errorf("read devshard storage identity: %w", err)
	}
	if identity == "" {
		return "", errors.New("devshard storage identity is empty")
	}
	return identity, nil
}
