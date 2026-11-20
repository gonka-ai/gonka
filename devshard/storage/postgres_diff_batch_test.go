package storage

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func newBatchPostgres(t *testing.T) *Postgres {
	t.Helper()
	pg := newTestPostgres(t)
	require.NoError(t, pg.CreateSession(defaultParams()))
	return pg
}

func countDiffRows(t *testing.T, pg *Postgres) (diffs, sigs int, latest uint64) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, pg.pool.QueryRow(ctx, `SELECT count(*) FROM devshard_diffs WHERE escrow_id = 'escrow-1'`).Scan(&diffs))
	require.NoError(t, pg.pool.QueryRow(ctx, `SELECT count(*) FROM devshard_signatures WHERE escrow_id = 'escrow-1'`).Scan(&sigs))
	meta, err := pg.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	return diffs, sigs, meta.LatestNonce
}

// holdPool takes every connection the pool may open and returns them.
func holdPool(t *testing.T, pool *pgxpool.Pool) []*pgxpool.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var held []*pgxpool.Conn
	for range pool.Config().MaxConns {
		c, err := pool.Acquire(ctx)
		require.NoError(t, err)
		held = append(held, c)
	}
	return held
}

// Test flow:
//  1. Hold every pooled connection, as a burst of other writers would.
//  2. Start a 200-diff batch; it must wait, not fail.
//  3. Free one connection; the batch commits in one transaction.
func TestPostgresDiffBatch_WaitsForAConnectionThenCommitsOnce(t *testing.T) {
	pg := newBatchPostgres(t)
	var commits atomic.Int32
	pgDiffBatchBeforeCommit = func(context.Context, pgx.Tx) error { commits.Add(1); return nil }
	t.Cleanup(func() { pgDiffBatchBeforeCommit = nil })

	held := holdPool(t, pg.pool)
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- PersistDiffBatch(ctx, pg, "escrow-1", batchRecs(1, 200))
	}()

	select {
	case err := <-done:
		t.Fatalf("batch finished while the pool was full: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	held[0].Release()
	for _, c := range held[1:] {
		defer c.Release()
	}

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("batch did not commit after a connection freed")
	}
	require.Equal(t, int32(1), commits.Load(), "a 200-diff batch is one transaction")
	diffs, sigs, latest := countDiffRows(t, pg)
	require.Equal(t, 200, diffs)
	require.Equal(t, 200, sigs)
	require.Equal(t, uint64(200), latest)
}

func TestPostgresDiffBatch_PoolFullPastDeadlineIsStorageBusy(t *testing.T) {
	pg := newBatchPostgres(t)
	held := holdPool(t, pg.pool)
	defer func() {
		for _, c := range held {
			c.Release()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	err := PersistDiffBatch(ctx, pg, "escrow-1", batchRecs(1, 10))
	require.ErrorIs(t, err, ErrStorageBusy)

	for _, c := range held {
		c.Release()
	}
	held = nil
	diffs, _, latest := countDiffRows(t, pg)
	require.Zero(t, diffs)
	require.Zero(t, latest)
}

func TestPostgresDiffBatch_CanceledWhileWaitingWritesNothing(t *testing.T) {
	pg := newBatchPostgres(t)
	held := holdPool(t, pg.pool)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	err := PersistDiffBatch(ctx, pg, "escrow-1", batchRecs(1, 10))
	require.ErrorIs(t, err, context.Canceled)

	for _, c := range held {
		c.Release()
	}
	diffs, _, latest := countDiffRows(t, pg)
	require.Zero(t, diffs)
	require.Zero(t, latest)
}

// Test flow:
//  1. Kill the batch's backend from outside just before COMMIT.
//  2. PersistDiffBatch retries on a new connection.
//  3. Every diff and signature is stored exactly once.
func TestPostgresDiffBatch_ConnectionDropMidTransactionRetries(t *testing.T) {
	pg := newBatchPostgres(t)
	var calls atomic.Int32
	pgDiffBatchBeforeCommit = func(ctx context.Context, tx pgx.Tx) error {
		if calls.Add(1) > 1 {
			return nil
		}
		var pid int
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			return err
		}
		killer, err := pgx.Connect(context.Background(), "")
		if err != nil {
			return err
		}
		defer killer.Close(context.Background())
		_, err = killer.Exec(context.Background(), `SELECT pg_terminate_backend($1)`, pid)
		return err
	}
	t.Cleanup(func() { pgDiffBatchBeforeCommit = nil })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, PersistDiffBatch(ctx, pg, "escrow-1", batchRecs(1, 50)))
	require.GreaterOrEqual(t, calls.Load(), int32(2), "the dropped transaction was retried")

	diffs, sigs, latest := countDiffRows(t, pg)
	require.Equal(t, 50, diffs)
	require.Equal(t, 50, sigs)
	require.Equal(t, uint64(50), latest)
}

func TestPostgresDiffBatch_ReplayIsIdempotentAndForkWritesNothing(t *testing.T) {
	pg := newBatchPostgres(t)
	ctx := context.Background()
	require.NoError(t, pg.AppendDiffBatch(ctx, "escrow-1", batchRecs(1, 20)))
	require.NoError(t, pg.AppendDiffBatch(ctx, "escrow-1", batchRecs(1, 30)),
		"a resend overlapping the durable prefix succeeds")

	forked := batchRecs(25, 40)
	forked[3].UserSig = []byte("forked")
	err := pg.AppendDiffBatch(ctx, "escrow-1", forked)
	require.ErrorIs(t, err, ErrDiffFork)

	diffs, sigs, latest := countDiffRows(t, pg)
	require.Equal(t, 30, diffs, "the forked batch wrote none of its new nonces")
	require.Equal(t, 30, sigs)
	require.Equal(t, uint64(30), latest)
}
