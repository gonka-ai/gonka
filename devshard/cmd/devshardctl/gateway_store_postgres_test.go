package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGatewayPGOpTimeoutEnv(t *testing.T) {
	t.Setenv("PG_OPERATION_TIMEOUT", "1500ms")
	require.Equal(t, 1500*time.Millisecond, gatewayPGOpTimeout())

	t.Setenv("PG_OPERATION_TIMEOUT", "0")
	require.Equal(t, time.Duration(0), gatewayPGOpTimeout())

	t.Setenv("PG_OPERATION_TIMEOUT", "not-a-duration")
	require.Equal(t, defaultGatewayPGOpTimeout, gatewayPGOpTimeout())

	t.Setenv("PG_OPERATION_TIMEOUT", "")
	require.Equal(t, defaultGatewayPGOpTimeout, gatewayPGOpTimeout())
}

func TestPostgresGatewayStoreOpCtx(t *testing.T) {
	s := &PostgresGatewayStore{opTimeout: 50 * time.Millisecond}
	ctx, cancel := s.opCtx()
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.InDelta(t, time.Now().Add(50*time.Millisecond).UnixNano(), deadline.UnixNano(), float64(25*time.Millisecond))

	disabled := &PostgresGatewayStore{opTimeout: 0}
	ctx2, cancel2 := disabled.opCtx()
	defer cancel2()
	_, ok = ctx2.Deadline()
	require.False(t, ok)
}

func TestPostgresGatewayStoreOpTimeoutCancelsSlowQuery(t *testing.T) {
	cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	pg, err := NewPostgresGatewayStore(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Close()) })
	pg.opTimeout = 100 * time.Millisecond

	ctx, cancel := pg.opCtx()
	defer cancel()
	_, err = pg.pool.Exec(ctx, `SELECT pg_sleep(5)`)
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded), "expected deadline exceeded, got: %v", err)
}
