package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPostgres_OpCtxHasDeadline verifies the Go-side per-operation bound exists
// and is set to postgresOpTimeout. opCtx does not touch the pool, so this runs
// without a container.
func TestPostgres_OpCtxHasDeadline(t *testing.T) {
	s := &Postgres{}
	start := time.Now()
	ctx, cancel := s.opCtx()
	defer cancel()

	deadline, ok := ctx.Deadline()
	require.True(t, ok, "opCtx must return a context with a deadline")
	remaining := time.Until(deadline)
	require.Positive(t, remaining)
	require.LessOrEqual(t, remaining, postgresOpTimeout)
	require.Greater(t, remaining, postgresOpTimeout-time.Since(start)-time.Second)
}

// TestPostgres_TimeoutsConfigured proves the server-side runtime params are
// applied to pooled connections.
func TestPostgres_TimeoutsConfigured(t *testing.T) {
	pg := newTestPostgres(t)
	ctx, cancel := pg.opCtx()
	defer cancel()

	var statementTimeout string
	require.NoError(t, pg.pool.QueryRow(ctx, "SHOW statement_timeout").Scan(&statementTimeout))
	require.Equal(t, "5s", statementTimeout)

	var lockTimeout string
	require.NoError(t, pg.pool.QueryRow(ctx, "SHOW lock_timeout").Scan(&lockTimeout))
	require.Equal(t, "3s", lockTimeout)
}

// TestPostgres_StatementTimeoutAborts proves a stuck query is aborted server-side
// rather than hanging. pg_sleep(30) far exceeds statement_timeout (5s); the call
// must return an error well before the sleep would complete.
func TestPostgres_StatementTimeoutAborts(t *testing.T) {
	pg := newTestPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	start := time.Now()
	_, err := pg.pool.Exec(ctx, "SELECT pg_sleep(30)")
	elapsed := time.Since(start)

	require.Error(t, err, "stuck query must be aborted")
	require.Less(t, elapsed, 15*time.Second, "abort must happen near statement_timeout, not after the full sleep")
}
