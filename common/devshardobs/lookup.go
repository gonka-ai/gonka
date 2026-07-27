package devshardobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionVersionLookup resolves which protocol version owns a bound escrow.
// ok=false means no active session row in Postgres (may still exist on SQLite).
type SessionVersionLookup interface {
	LookupSessionVersion(ctx context.Context, escrowID string) (version string, ok bool, err error)
}

// Lookup resolves escrow → bound protocol version from shared Postgres session
// metadata (devshard_session_index + devshard_sessions).
type Lookup struct {
	pool *pgxpool.Pool
}

// OpenLookupFromEnv connects using libpq env (PGHOST/PGUSER/… or DATABASE_URL).
// Returns (nil, nil) when Postgres is not configured or lookup is disabled.
func OpenLookupFromEnv(ctx context.Context) (*Lookup, error) {
	if lookupDisabled() {
		slog.Info("devshardobs: session version lookup disabled")
		return nil, nil
	}
	if !postgresConfigured() {
		slog.Info("devshardobs: session version lookup disabled (no PGHOST/DATABASE_URL); fan-out only")
		return nil, nil
	}

	connString := os.Getenv("DATABASE_URL")
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
	slog.Info("devshardobs: session version lookup enabled (postgres)")
	return &Lookup{pool: pool}, nil
}

func lookupDisabled() bool {
	v := os.Getenv("DEVSHARD_OBS_DISABLE_SESSION_LOOKUP")
	if v == "" {
		v = os.Getenv("VERSIOND_DISABLE_SESSION_LOOKUP")
	}
	return strings.EqualFold(v, "true") || v == "1"
}

func postgresConfigured() bool {
	if os.Getenv("DATABASE_URL") != "" {
		return true
	}
	return os.Getenv("PGHOST") != "" || os.Getenv("PGDATABASE") != ""
}

// Close releases the pool.
func (l *Lookup) Close() {
	if l != nil && l.pool != nil {
		l.pool.Close()
	}
}

// LookupSessionVersion implements SessionVersionLookup.
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
