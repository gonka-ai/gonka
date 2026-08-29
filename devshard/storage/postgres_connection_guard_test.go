package storage

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestPostgresConnectionGuardDisablesIdleSessionTimeout(t *testing.T) {
	container := startPostgresContainer(t)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	cfg, err := pgxpool.ParseConfig("")
	require.NoError(t, err)
	guard, err := newPostgresConnectionGuard()
	require.NoError(t, err)
	guard.installValidator(cfg)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	require.NoError(t, guard.arm(context.Background(), pool, cfg.ConnConfig))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		guard.close(ctx)
	})

	var idleTimeout string
	require.NoError(t, guard.fenceConn.QueryRow(context.Background(),
		"SHOW idle_session_timeout").Scan(&idleTimeout))
	require.Equal(t, "0", idleTimeout)
}
