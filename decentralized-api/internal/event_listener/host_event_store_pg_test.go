package event_listener

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"decentralized-api/apiconfig"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestHostEventStore_Postgres(t *testing.T) {
	dsn := startHostEventPostgres(t)
	runHostEventStoreSuite(t, func(t *testing.T, writer string) *dbStore {
		t.Helper()
		s, err := dialHostEventPostgres(t, dsn, writer)
		require.NoError(t, err)
		s.generation = 4
		return s
	})
}

func TestHostEventStore_PostgresSynchronousCommit(t *testing.T) {
	dsn := startHostEventPostgres(t)
	s, err := dialHostEventPostgres(t, dsn, "sync-writer")
	require.NoError(t, err)
	s.mu.Lock()
	_, err = s.conn.ExecContext(t.Context(), "SET synchronous_commit = off")
	s.mu.Unlock()
	require.NoError(t, err)

	var seen string
	s.inTx = func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SHOW synchronous_commit").Scan(&seen)
	}
	_, err = s.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, at(s, 1, 0))
	require.NoError(t, err)
	require.Equal(t, "on", seen)

	var after string
	s.mu.Lock()
	err = s.conn.QueryRowContext(t.Context(), "SHOW synchronous_commit").Scan(&after)
	s.mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, "off", after)
}

func TestHostEventStore_PostgresWriterLock(t *testing.T) {
	dsn := startHostEventPostgres(t)
	first, err := dialHostEventPostgres(t, dsn, "same-writer")
	require.NoError(t, err)
	second, err := dialHostEventPostgres(t, dsn, "same-writer")
	require.ErrorIs(t, err, ErrWriterLocked)

	_, err = second.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, at(second, 1, 0))
	require.ErrorIs(t, err, ErrWriterLocked)

	rpc := newFeedRPC()
	epochs := &recordingEpochs{inner: mapEpochs{
		latestIdx:   5,
		latestStart: 100,
		ok:          true,
		starts:      map[uint64]int64{4: 1},
	}}
	f, bo, ring := newTestFeed(t, rpc, epochs, nil)
	f.store = second
	f.writer = second.Writer()
	f.markerPath = markerPath(t.TempDir())
	f.writeHead = func(m headMarker) error { return writeHeadMarkerFile(f.markerPath, m) }
	bo.updateStatus(3, true)
	f.walk(t.Context())
	require.True(t, f.Stalled())
	require.False(t, f.RingRestored())
	require.Equal(t, uint64(0), ring.Head())
	require.Zero(t, rpc.blockResultCalls(3))
	require.Zero(t, rpc.headerCalls)
	require.Zero(t, rpc.biCalls)
	require.Zero(t, rpc.statusCalls)
	require.Zero(t, epochs.starts)

	other, err := dialHostEventPostgres(t, dsn, "other-writer")
	require.NoError(t, err)
	_, err = other.AppendHeight(t.Context(), 2, 1, []apiconfig.HostEvent{blockOnly(2, 1)}, at(other, 1, 1))
	require.NoError(t, err)
	_, err = first.AppendHeight(t.Context(), 4, 1, []apiconfig.HostEvent{blockOnly(4, 1)}, at(first, 1, 3))
	require.NoError(t, err)
	page, _, err := second.Read(t.Context(), 0, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock}, 10)
	require.NoError(t, err)
	require.NotEmpty(t, page)
	head, err := second.Head(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(4), head.Height)
	_, found, _, err := second.ResolveBlock(t.Context(), 4)
	require.NoError(t, err)
	require.True(t, found)
	snap, err := second.Load(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(4), snap.EmittedHeight)
}

func TestHostEventStore_PostgresPingReleasesWriterLock(t *testing.T) {
	dsn := startHostEventPostgres(t)
	s, err := dialHostEventPostgres(t, dsn, "ping-writer")
	require.NoError(t, err)
	s.generation = 4
	_, err = s.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, at(s, 1, 0))
	require.NoError(t, err)

	var pid int
	s.mu.Lock()
	err = s.conn.QueryRowContext(t.Context(), "SELECT pg_backend_pid()").Scan(&pid)
	s.mu.Unlock()
	require.NoError(t, err)

	stmtErr := errors.New("injected connection failure")
	s.stmtErr = stmtErr
	_, err = s.AppendHeight(t.Context(), 2, 1, []apiconfig.HostEvent{blockOnly(2, 1)}, at(s, 2, 1))
	require.ErrorIs(t, err, stmtErr)
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	require.Nil(t, conn)

	raw, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer raw.Close()
	var alive bool
	err = raw.QueryRowContext(t.Context(), "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = $1)", pid).Scan(&alive)
	require.NoError(t, err)
	require.False(t, alive, "ping failure left the locked backend alive")
	var got bool
	err = raw.QueryRowContext(t.Context(), "SELECT pg_try_advisory_lock($1)", s.lockKey).Scan(&got)
	require.NoError(t, err)
	require.True(t, got, "writer lock still held after ping failure")
	_, err = raw.ExecContext(t.Context(), "SELECT pg_advisory_unlock($1)", s.lockKey)
	require.NoError(t, err)

	_, err = s.AppendHeight(t.Context(), 2, 1, []apiconfig.HostEvent{blockOnly(2, 1)}, at(s, 2, 1))
	require.NoError(t, err)
	require.Greater(t, s.reloads, 0)
}

func TestHostEventStore_PostgresReconnectReloads(t *testing.T) {
	dsn := startHostEventPostgres(t)
	s, err := dialHostEventPostgres(t, dsn, "reload-writer")
	require.NoError(t, err)

	f, bo, ring := newTestFeed(t, newFeedRPC(), defaultEpochs(), nil)
	f.store = s
	f.writer = s.Writer()
	f.markerPath = markerPath(t.TempDir())
	f.writeHead = func(m headMarker) error { return writeHeadMarkerFile(f.markerPath, m) }
	bootHostEventFeed(t.Context(), f)
	bo.updateStatus(1, true)
	f.walk(t.Context())
	require.False(t, f.Stalled())
	gen := ring.Generation()
	require.Equal(t, []int64{1}, heightsOf(ringEvents(t, ring)))

	raw, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer raw.Close()
	var pid int
	s.mu.Lock()
	err = s.conn.QueryRowContext(t.Context(), "SELECT pg_backend_pid()").Scan(&pid)
	s.mu.Unlock()
	require.NoError(t, err)
	_, err = raw.ExecContext(t.Context(), "UPDATE host_event_feed SET next_seq = 50 WHERE writer = $1", s.writer)
	require.NoError(t, err)
	_, err = raw.ExecContext(t.Context(), "SELECT pg_terminate_backend($1)", pid)
	require.NoError(t, err)

	bo.updateStatus(2, true)
	f.walk(t.Context())
	require.True(t, f.Stalled())
	require.Equal(t, gen, ring.Generation())
	require.Equal(t, uint64(1), ring.Head())

	f.walk(t.Context())
	require.False(t, f.Stalled())
	require.Greater(t, s.reloads, 0)
	require.NotEqual(t, gen, ring.Generation())
	got := ringEvents(t, ring)
	require.Equal(t, []int64{2}, heightsOf(got))
	require.Equal(t, uint64(1), got[0].Seq)
	require.NotEqual(t, gen, mustGeneration(t, s))
	old := ring.Since(1, gen, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock})
	require.True(t, old.Reset)
	_, found, _, err := s.ResolveBlock(t.Context(), 1)
	require.NoError(t, err)
	require.False(t, found)
}

func TestHostEventStore_PostgresRepeatableRead(t *testing.T) {
	dsn := startHostEventPostgres(t)

	t.Run("Load", func(t *testing.T) {
		s, err := dialHostEventPostgres(t, dsn, "rr-load")
		require.NoError(t, err)
		s.generation = 4
		_, err = s.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, at(s, 1, 0))
		require.NoError(t, err)

		s.betweenRead = func() {
			s.betweenRead = nil
			_, err := s.AppendHeight(t.Context(), 2, 1, []apiconfig.HostEvent{blockOnly(2, 1)}, at(s, 2, 1))
			require.NoError(t, err)
		}
		snap, err := s.Load(t.Context())
		require.NoError(t, err)
		require.NotEmpty(t, snap.Tail)
		require.Equal(t, snap.Tail[len(snap.Tail)-1].Seq+1, snap.NextSeq)
		require.Equal(t, uint64(1), snap.Tail[len(snap.Tail)-1].Seq)
		require.Equal(t, int64(1), snap.EmittedHeight)

		after, err := s.Load(t.Context())
		require.NoError(t, err)
		require.Equal(t, uint64(3), after.NextSeq)
		require.Equal(t, int64(2), after.EmittedHeight)
	})

	t.Run("Head", func(t *testing.T) {
		s, err := dialHostEventPostgres(t, dsn, "rr-head")
		require.NoError(t, err)
		s.generation = 4
		_, err = s.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, at(s, 1, 0))
		require.NoError(t, err)

		s.betweenRead = func() {
			s.betweenRead = nil
			_, err := s.AppendHeight(t.Context(), 2, 1, []apiconfig.HostEvent{blockOnly(2, 1)}, at(s, 2, 1))
			require.NoError(t, err)
		}
		head, err := s.Head(t.Context())
		require.NoError(t, err)
		require.Equal(t, uint64(1), head.Seq)
		require.Equal(t, int64(1), head.Height)

		after, err := s.Head(t.Context())
		require.NoError(t, err)
		require.Equal(t, uint64(2), after.Seq)
		require.Equal(t, int64(2), after.Height)
	})
}

func TestHostEventStore_PostgresResolveBlockUsesPartialIndex(t *testing.T) {
	dsn := startHostEventPostgres(t)
	s, err := dialHostEventPostgres(t, dsn, "explain-writer")
	require.NoError(t, err)
	s.generation = 1
	next := uint64(1)
	for h := int64(1); h <= 50; h++ {
		events := heightRows(h, 1)
		_, err = s.AppendHeight(t.Context(), h, 1, events, at(s, next, h-1))
		require.NoError(t, err)
		next += uint64(len(events))
	}
	conn, err := s.db.Conn(t.Context())
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(t.Context(), "ANALYZE host_events")
	require.NoError(t, err)
	_, err = conn.ExecContext(t.Context(), "SET plan_cache_mode = force_generic_plan")
	require.NoError(t, err)
	_, err = conn.ExecContext(t.Context(), "SET enable_seqscan = off")
	require.NoError(t, err)
	// On a small table a bitmap scan of host_events_height is cheaper than an index
	// scan of the partial index, even when kind is inlined. Index scans are what
	// show that the partial predicate matched.
	_, err = conn.ExecContext(t.Context(), "SET enable_bitmapscan = off")
	require.NoError(t, err)
	_, err = conn.ExecContext(t.Context(), "PREPARE resolve_block AS "+s.q(resolveBlockSelect()))
	require.NoError(t, err)
	// EXPLAIN is a utility statement. pgx's extended protocol describes it as taking
	// no parameters, so the prepared statement's arguments have to be inlined.
	lit := "'" + strings.ReplaceAll(s.writer, "'", "''") + "'"
	rows, err := conn.QueryContext(t.Context(), fmt.Sprintf("EXPLAIN EXECUTE resolve_block(%s, %d)", lit, 10))
	require.NoError(t, err)
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		if plan.Len() > 0 {
			plan.WriteByte('\n')
		}
		plan.WriteString(line)
	}
	require.NoError(t, rows.Err())
	require.Contains(t, plan.String(), "host_events_block_height")
}

func startHostEventPostgres(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping postgres host event store tests in -short mode")
	}
	ctx := context.Background()
	container, err := postgres.Run(ctx,
		"postgres:18.1-bookworm",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Skipf("postgres container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432")
	require.NoError(t, err)
	return fmt.Sprintf("postgres://testuser:testpass@%s:%s/testdb?sslmode=disable", host, port.Port())
}

func dialHostEventPostgres(t *testing.T, dsn, writer string) (*dbStore, error) {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	db := stdlib.OpenDB(*cfg)
	s := &dbStore{dialect: "postgres", writer: writer, db: db, lockKey: writerLockKey(writer)}
	if err := s.pingAndSchema(t.Context()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.ensureConn(t.Context()); err != nil {
		if errors.Is(err, ErrWriterLocked) {
			t.Cleanup(func() { _ = s.Close() })
			return s, err
		}
		_ = db.Close()
		return nil, err
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, nil
}

func mustGeneration(t *testing.T, s *dbStore) uint64 {
	t.Helper()
	snap, err := s.Load(t.Context())
	require.NoError(t, err)
	return snap.Generation
}
