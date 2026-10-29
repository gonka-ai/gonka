package headertime

import (
	"context"
	"fmt"
	"time"

	"common/storage/pgpool"
	"common/storage/pgtimeouts"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const postgresSchema = `
CREATE TABLE IF NOT EXISTS devshard_header_times (
  height BIGINT PRIMARY KEY,
  hash BYTEA NOT NULL,
  time_ms BIGINT NOT NULL,
  origin TEXT NOT NULL,
  verified BOOLEAN NOT NULL,
  epoch BIGINT NOT NULL DEFAULT 0
);
`

type pgCache struct {
	pool      *pgxpool.Pool
	opTimeout time.Duration
}

func openPostgresCache(ctx context.Context) (*pgCache, error) {
	connectTimeout := pgtimeouts.ConnectTimeout()
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	cfg, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, fmt.Errorf("parse header time postgres config: %w", err)
	}
	pgtimeouts.ApplyConnConfig(cfg.ConnConfig)
	if err := pgpool.ConfigureMaxConns(cfg); err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(connectCtx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect header time postgres: %w", err)
	}
	if err := pool.Ping(connectCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping header time postgres: %w", err)
	}
	c := &pgCache{pool: pool, opTimeout: pgtimeouts.OperationTimeout()}
	if _, err := pool.Exec(connectCtx, postgresSchema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("init header time postgres schema: %w", err)
	}
	return c, nil
}

func (c *pgCache) opCtx(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if c == nil || c.opTimeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, c.opTimeout)
}

func (c *pgCache) Close() error {
	if c == nil || c.pool == nil {
		return nil
	}
	c.pool.Close()
	return nil
}

func (c *pgCache) Load(ctx context.Context) ([]Row, error) {
	if c == nil || c.pool == nil {
		return nil, nil
	}
	loadCtx, cancel := context.WithTimeout(ctx, pgtimeouts.ImportTimeout())
	defer cancel()
	rs, err := c.pool.Query(loadCtx, `SELECT height, hash, time_ms, origin, verified, epoch FROM devshard_header_times ORDER BY height`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []Row
	for rs.Next() {
		var r Row
		if err := rs.Scan(&r.Height, &r.Hash, &r.TimeMs, &r.Origin, &r.Verified, &r.Epoch); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func (c *pgCache) Save(ctx context.Context, rows []Row) error {
	if c == nil || c.pool == nil || len(rows) == 0 {
		return nil
	}
	opCtx, cancel := c.opCtx(ctx)
	defer cancel()
	batch := &pgx.Batch{}
	for _, r := range rows {
		if r.Height == 0 {
			continue
		}
		batch.Queue(
			`INSERT INTO devshard_header_times (height, hash, time_ms, origin, verified, epoch)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (height) DO UPDATE SET
			   hash = EXCLUDED.hash,
			   time_ms = EXCLUDED.time_ms,
			   origin = EXCLUDED.origin,
			   verified = EXCLUDED.verified,
			   epoch = EXCLUDED.epoch`,
			int64(r.Height), r.Hash, r.TimeMs, string(r.Origin), r.Verified, int64(r.Epoch),
		)
	}
	if batch.Len() == 0 {
		return nil
	}
	br := c.pool.SendBatch(opCtx, batch)
	defer br.Close()
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

func (c *pgCache) DeleteBelow(ctx context.Context, floor uint64) error {
	if c == nil || c.pool == nil || floor == 0 {
		return nil
	}
	opCtx, cancel := c.opCtx(ctx)
	defer cancel()
	_, err := c.pool.Exec(opCtx, `DELETE FROM devshard_header_times WHERE height < $1`, int64(floor))
	return err
}
