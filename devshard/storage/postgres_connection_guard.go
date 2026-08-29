package storage

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// postgresConnectionGuard binds one application pool to the writable database
// selected during startup. Every later connection must observe this process's
// token, including connections created after DNS or load-balancer changes.
type postgresConnectionGuard struct {
	token string
	armed atomic.Bool
}

func newPostgresConnectionGuard() *postgresConnectionGuard {
	return &postgresConnectionGuard{token: uuid.NewString()}
}

func (g *postgresConnectionGuard) installValidator(cfg *pgxpool.Config) {
	previous := cfg.ConnConfig.ValidateConnect
	cfg.ConnConfig.ValidateConnect = func(ctx context.Context, conn *pgconn.PgConn) error {
		if previous != nil {
			if err := previous(ctx, conn); err != nil {
				return err
			}
		}
		if !g.armed.Load() {
			return nil
		}
		result := conn.ExecParams(ctx, `
SELECT EXISTS (
    SELECT 1 FROM devshard_connection_lineage WHERE token = $1::uuid
) AND NOT pg_is_in_recovery()`, [][]byte{[]byte(g.token)}, nil, nil, nil).Read()
		if result.Err != nil {
			return fmt.Errorf("verify postgres connection lineage: %w", result.Err)
		}
		if len(result.Rows) != 1 || len(result.Rows[0]) != 1 {
			return errors.New("verify postgres connection lineage: unexpected query result")
		}
		if string(result.Rows[0][0]) != "t" {
			return errors.New("postgres connection does not belong to the initialized writable database")
		}
		return nil
	}
}

func (g *postgresConnectionGuard) arm(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
INSERT INTO devshard_connection_lineage (token) VALUES ($1::uuid)
ON CONFLICT (token) DO NOTHING`, g.token); err != nil {
		return fmt.Errorf("seed postgres connection lineage: %w", err)
	}
	g.armed.Store(true)
	// Initialization has no concurrent application users. Reset removes every
	// connection opened before the validator was armed, so all serving traffic
	// goes through the connection-level check.
	pool.Reset()
	return nil
}

func (g *postgresConnectionGuard) remove(ctx context.Context, pool *pgxpool.Pool) {
	if g == nil || pool == nil || !g.armed.Load() {
		return
	}
	_, _ = pool.Exec(ctx,
		`DELETE FROM devshard_connection_lineage WHERE token = $1::uuid`, g.token)
}
