package storage

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// postgresConnectionGuard binds one application pool to the writable database
// selected during startup. The session advisory lock is deliberately outside
// durable storage: WAL replication cannot copy it to a promoted fork.
type postgresConnectionGuard struct {
	key       int32
	armed     atomic.Bool
	fenceConn *pgx.Conn
}

const postgresConnectionGuardNamespace int32 = 0x474f4e4b // "GONK"

func newPostgresConnectionGuard() (*postgresConnectionGuard, error) {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, fmt.Errorf("generate postgres connection fence: %w", err)
	}
	key := int32(binary.BigEndian.Uint32(raw[:]) & math.MaxInt32)
	if key == 0 {
		key = 1
	}
	return &postgresConnectionGuard{key: key}, nil
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
    SELECT 1
    FROM pg_locks
    WHERE locktype = 'advisory'
      AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
      AND classid = $1::bigint::oid
      AND objid = $2::bigint::oid
      AND objsubid = 2
      AND granted
) AND NOT pg_is_in_recovery()`, [][]byte{
			[]byte(fmt.Sprint(postgresConnectionGuardNamespace)),
			[]byte(fmt.Sprint(g.key)),
		}, nil, nil, nil).Read()
		if result.Err != nil {
			return fmt.Errorf("verify postgres connection fence: %w", result.Err)
		}
		if len(result.Rows) != 1 || len(result.Rows[0]) != 1 {
			return errors.New("verify postgres connection fence: unexpected query result")
		}
		if string(result.Rows[0][0]) != "t" {
			return errors.New("postgres connection does not belong to the fenced writable database")
		}
		return nil
	}
}

func (g *postgresConnectionGuard) arm(ctx context.Context, pool *pgxpool.Pool, cfg *pgx.ConnConfig) error {
	conn, err := pgx.ConnectConfig(ctx, cfg.Copy())
	if err != nil {
		return fmt.Errorf("connect postgres fence session: %w", err)
	}
	if _, err := conn.Exec(ctx,
		`SELECT pg_advisory_lock($1, $2)`, postgresConnectionGuardNamespace, g.key); err != nil {
		_ = conn.Close(context.Background())
		return fmt.Errorf("acquire postgres connection fence: %w", err)
	}
	g.fenceConn = conn
	g.armed.Store(true)
	// Initialization has no concurrent application users. Reset removes every
	// connection opened before the fence was armed, so all serving traffic goes
	// through the connection-level check.
	pool.Reset()
	return nil
}

func (g *postgresConnectionGuard) close(ctx context.Context) {
	if g == nil || g.fenceConn == nil {
		return
	}
	g.armed.Store(false)
	_ = g.fenceConn.Close(ctx)
	g.fenceConn = nil
}
