package rpcserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/storage"
	"devshard/storage/pgtest"
)

const haTestVersion = "v2"

type queryTrace struct {
	mu     sync.Mutex
	events []tracedQuery
}

type tracedQuery struct {
	sql  string
	args []any
}

func (q *queryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	args := append([]any(nil), data.Args...)
	for i, arg := range args {
		if b, ok := arg.([]byte); ok {
			args[i] = append([]byte(nil), b...)
		}
	}
	q.mu.Lock()
	q.events = append(q.events, tracedQuery{sql: data.SQL, args: args})
	q.mu.Unlock()
	return ctx
}

func (q *queryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (q *queryTrace) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.events)
}

func (q *queryTrace) hasHashSince(n int, hash []byte) bool {
	hexed := hex.EncodeToString(hash)
	q.mu.Lock()
	defer q.mu.Unlock()
	if n > len(q.events) {
		n = len(q.events)
	}
	for _, ev := range q.events[n:] {
		if strings.Contains(ev.sql, hexed) {
			return true
		}
		for _, arg := range ev.args {
			b, ok := arg.([]byte)
			if ok && bytes.Equal(b, hash) {
				return true
			}
		}
	}
	return false
}

func setupPeerRPCPostgres(t *testing.T) (*pgxpool.Pool, *queryTrace, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping peer rpc session postgres tests in -short mode")
	}
	ctx := context.Background()
	container := pgtest.MustStart(t, ctx)
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	t.Setenv("PGHOST", host)
	t.Setenv("PGPORT", port.Port())
	t.Setenv("PGDATABASE", "testdb")
	t.Setenv("PGUSER", "testuser")
	t.Setenv("PGPASSWORD", "testpass")

	tracer := &queryTrace{}
	cfg, err := pgxpool.ParseConfig("")
	require.NoError(t, err)
	cfg.MaxConns = 16
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	require.NoError(t, pool.Ping(ctx))
	require.NoError(t, storage.MigratePostgres(ctx, pool))
	return pool, tracer, func() {
		pool.Close()
		_ = container.Terminate(ctx)
	}
}

func truncatePeerRPC(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `TRUNCATE devshard_peer_rpc_sessions, devshard_peer_rpc_members`)
	require.NoError(t, err)
}

func openHAShared(pool *pgxpool.Pool) *SharedSessions {
	return OpenSharedSessions(pool, SharedConfig{HostAddress: testHostAddress, Version: haTestVersion})
}

func startHAHandler(t *testing.T, pool *pgxpool.Pool, cfg PeerAuthConfig) *PeerAuthHandler {
	t.Helper()
	if cfg.MaxSessions == 0 {
		cfg.MaxSessions = 10
	}
	h := newTestAuth(cfg)
	h.EnableShared(openHAShared(pool))
	t.Cleanup(h.Close)
	require.Eventually(t, h.SessionsReady, 10*time.Second, 20*time.Millisecond, "shared sessions did not become ready")
	return h
}

func nonceN(n byte) []byte {
	b := bytes.Repeat([]byte{n}, 16)
	return b
}

func countLiveSessions(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(), `
SELECT COUNT(*) FROM devshard_peer_rpc_sessions
WHERE host_address = $1 AND version = $2 AND state = 'live'`,
		testHostAddress, haTestVersion).Scan(&n)
	require.NoError(t, err)
	return n
}

func insertLiveRow(t *testing.T, pool *pgxpool.Pool, version, peer string, raw []byte) {
	t.Helper()
	sum := sha256.Sum256(raw)
	_, err := pool.Exec(context.Background(), `
INSERT INTO devshard_peer_rpc_sessions (
    token_hash, host_address, version, peer, attached_unix, expires_at, grace_until, state, seq, origin, updated_at
) VALUES (
    $1, $2, $3, $4, $5, now() + interval '5 minutes', NULL, 'live',
    nextval('devshard_peer_rpc_session_seq'), 'test', now()
)`, sum[:], testHostAddress, version, peer, time.Now().Unix())
	require.NoError(t, err)
}

func TestSharedSessionsPostgres(t *testing.T) {
	pool, tracer, cleanup := setupPeerRPCPostgres(t)
	t.Cleanup(cleanup)
	ensureSessionHAMetrics()

	t.Run("startup_load_and_version_filter", func(t *testing.T) {
		truncatePeerRPC(t, pool)
		raw := nonceN(0x11)
		other := nonceN(0x22)
		insertLiveRow(t, pool, haTestVersion, "peer-loaded", raw)
		insertLiveRow(t, pool, "v9", "peer-other", other)
		s := openHAShared(pool)
		require.False(t, s.Ready())
		h := newTestAuth(PeerAuthConfig{MaxSessions: 10})
		h.EnableShared(s)
		t.Cleanup(h.Close)
		require.Eventually(t, h.SessionsReady, 10*time.Second, 20*time.Millisecond)
		peer, ok := h.LookupToken(raw)
		require.True(t, ok)
		require.Equal(t, "peer-loaded", peer)
		_, ok = h.LookupToken(other)
		require.False(t, ok, "other version must not be admitted")
	})

	t.Run("two_handlers_and_unknown_token", func(t *testing.T) {
		truncatePeerRPC(t, pool)
		a := startHAHandler(t, pool, PeerAuthConfig{})
		b := startHAHandler(t, pool, PeerAuthConfig{})
		signer := testutil.MustGenerateKey(t)
		ts := time.Now().Unix()
		resp, err := attachDirectAt(t, a, signer, nonceN(0x31), ts)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			peer, ok := b.LookupToken(resp.SessionToken)
			return ok && peer == signer.Address()
		}, 5*time.Second, 20*time.Millisecond, "other child did not apply the session")

		_, stop, err := b.beginWatch(resp.SessionToken)
		require.NoError(t, err)
		_, err = attachDirectAt(t, a, signer, nonceN(0x32), ts+1)
		require.NoError(t, err)
		select {
		case <-stop:
		case <-time.After(5 * time.Second):
			t.Fatal("replace on one child must end Watch on the other")
		}

		junk := nonceN(0xee)
		sum := sha256.Sum256(junk)
		from := tracer.len()
		_, ok := b.LookupToken(junk)
		require.False(t, ok)
		require.False(t, tracer.hasHashSince(from, sum[:]), "unknown token must not be queried")
	})

	t.Run("stale_and_replay", func(t *testing.T) {
		truncatePeerRPC(t, pool)
		h := startHAHandler(t, pool, PeerAuthConfig{})
		signer := testutil.MustGenerateKey(t)
		ts := time.Now().Unix()
		first, err := attachDirectAt(t, h, signer, nonceN(0x41), ts)
		require.NoError(t, err)
		_, err = attachDirectAt(t, h, signer, nonceN(0x42), ts-1)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not newer")
		peer, ok := h.LookupToken(first.SessionToken)
		require.True(t, ok)
		require.Equal(t, signer.Address(), peer)

		second, err := attachDirectAt(t, h, signer, nonceN(0x43), ts+1)
		require.NoError(t, err)
		_, err = attachDirectAt(t, h, signer, nonceN(0x41), ts+2)
		require.Error(t, err)
		require.Contains(t, err.Error(), "already in use")
		_, ok = h.LookupToken(second.SessionToken)
		require.True(t, ok)
		require.Equal(t, 1, countLiveSessions(t, pool))
	})

	t.Run("grace_then_expiry", func(t *testing.T) {
		truncatePeerRPC(t, pool)
		h := startHAHandler(t, pool, PeerAuthConfig{TokenGrace: 2 * time.Second})
		signer := testutil.MustGenerateKey(t)
		ts := time.Now().Unix()
		old, err := attachDirectAt(t, h, signer, nonceN(0x51), ts)
		require.NoError(t, err)
		next, err := attachDirectAt(t, h, signer, nonceN(0x52), ts+1)
		require.NoError(t, err)
		_, ok := h.LookupToken(old.SessionToken)
		require.True(t, ok, "replaced token admits during TokenGrace")
		require.Eventually(t, func() bool {
			_, still := h.LookupToken(old.SessionToken)
			return !still
		}, 5*time.Second, 50*time.Millisecond, "grace token must expire")
		_, ok = h.LookupToken(next.SessionToken)
		require.True(t, ok)
	})

	t.Run("concurrent_one_live_row", func(t *testing.T) {
		truncatePeerRPC(t, pool)
		h := startHAHandler(t, pool, PeerAuthConfig{})
		signer := testutil.MustGenerateKey(t)
		ts := time.Now().Unix()
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for i, n := range []byte{0x61, 0x62} {
			wg.Add(1)
			go func(nonce byte, stamp int64) {
				defer wg.Done()
				_, err := attachDirectAt(t, h, signer, nonceN(nonce), stamp)
				errs <- err
			}(n, ts+int64(i))
		}
		wg.Wait()
		close(errs)
		okN := 0
		for err := range errs {
			if err == nil {
				okN++
			}
		}
		require.GreaterOrEqual(t, okN, 1)
		require.Equal(t, 1, countLiveSessions(t, pool))
	})

	t.Run("max_sessions_across_handlers", func(t *testing.T) {
		truncatePeerRPC(t, pool)
		a := startHAHandler(t, pool, PeerAuthConfig{MaxSessions: 2})
		b := startHAHandler(t, pool, PeerAuthConfig{MaxSessions: 2})
		ts := time.Now().Unix()
		for i, h := range []*PeerAuthHandler{a, b, a} {
			signer := testutil.MustGenerateKey(t)
			_, err := attachDirectAt(t, h, signer, nonceN(byte(0x70+i)), ts+int64(i))
			require.NoError(t, err)
		}
		require.Equal(t, 2, countLiveSessions(t, pool))
	})

	t.Run("barrier_timeout_then_clear", func(t *testing.T) {
		truncatePeerRPC(t, pool)
		h := startHAHandler(t, pool, PeerAuthConfig{})
		_, err := pool.Exec(context.Background(), `
INSERT INTO devshard_peer_rpc_members (
    instance_id, host_address, version, applied_seq, heartbeat_at, ready
) VALUES ('stuck', $1, $2, 0, now(), true)`, testHostAddress, haTestVersion)
		require.NoError(t, err)
		signer := testutil.MustGenerateKey(t)
		before := promtest.ToFloat64(barrierTimeouts)
		start := time.Now()
		_, err = attachDirectAt(t, h, signer, nonceN(0x81), time.Now().Unix())
		elapsed := time.Since(start)
		require.NoError(t, err)
		require.GreaterOrEqual(t, elapsed, 800*time.Millisecond)
		require.Less(t, elapsed, 3*time.Second)
		require.Equal(t, before+1, promtest.ToFloat64(barrierTimeouts))

		_, err = pool.Exec(context.Background(), `DELETE FROM devshard_peer_rpc_members WHERE instance_id = 'stuck'`)
		require.NoError(t, err)
		start = time.Now()
		_, err = attachDirectAt(t, h, signer, nonceN(0x82), time.Now().Unix()+1)
		require.NoError(t, err)
		require.Less(t, time.Since(start), 500*time.Millisecond, "barrier must return once every ready member has applied")
	})

	t.Run("listen_drop_catch_up", func(t *testing.T) {
		truncatePeerRPC(t, pool)
		a := startHAHandler(t, pool, PeerAuthConfig{})
		b := startHAHandler(t, pool, PeerAuthConfig{})
		before := promtest.ToFloat64(notifyReconnects)
		a.shared.DropListenForTest()
		signer := testutil.MustGenerateKey(t)
		resp, err := attachDirectAt(t, b, signer, nonceN(0x91), time.Now().Unix())
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			peer, ok := a.LookupToken(resp.SessionToken)
			return ok && peer == signer.Address()
		}, 5*time.Second, 20*time.Millisecond, "child did not catch up after LISTEN drop")
		require.GreaterOrEqual(t, promtest.ToFloat64(notifyReconnects), before+1)
	})

	t.Run("published_barrier_skips_unregistered_after_timeout", func(t *testing.T) {
		truncatePeerRPC(t, pool)
		h := startHAHandler(t, pool, PeerAuthConfig{})
		h.SetPublishedBarrier("self", []string{"self", "old-versiond"})
		signer := testutil.MustGenerateKey(t)
		start := time.Now()
		_, err := attachDirectAt(t, h, signer, nonceN(0xb1), time.Now().Unix())
		require.NoError(t, err)
		require.GreaterOrEqual(t, time.Since(start), 800*time.Millisecond, "an unpublished member is waited for once")
		start = time.Now()
		_, err = attachDirectAt(t, h, signer, nonceN(0xb2), time.Now().Unix()+1)
		require.NoError(t, err)
		require.Less(t, time.Since(start), 500*time.Millisecond, "a member that never registers must not stall every Attach")
	})

	t.Run("published_barrier_waits_for_registered_member", func(t *testing.T) {
		truncatePeerRPC(t, pool)
		h := startHAHandler(t, pool, PeerAuthConfig{})
		_, err := pool.Exec(context.Background(), `
INSERT INTO devshard_peer_rpc_members (
    instance_id, host_address, version, applied_seq, heartbeat_at, ready
) VALUES ('stuck', $1, $2, 0, now(), true)`, testHostAddress, haTestVersion)
		require.NoError(t, err)
		h.SetPublishedBarrier("self", []string{"self", "stuck"})
		signer := testutil.MustGenerateKey(t)
		start := time.Now()
		_, err = attachDirectAt(t, h, signer, nonceN(0xb3), time.Now().Unix())
		require.NoError(t, err)
		require.GreaterOrEqual(t, time.Since(start), 800*time.Millisecond)
		start = time.Now()
		_, err = attachDirectAt(t, h, signer, nonceN(0xb4), time.Now().Unix()+1)
		require.NoError(t, err)
		require.GreaterOrEqual(t, time.Since(start), 800*time.Millisecond, "a registered member that has not applied is waited for on every Attach")
	})

	t.Run("shutdown_keeps_rows", func(t *testing.T) {
		truncatePeerRPC(t, pool)
		h := startHAHandler(t, pool, PeerAuthConfig{})
		signer := testutil.MustGenerateKey(t)
		_, err := attachDirectAt(t, h, signer, nonceN(0xa1), time.Now().Unix())
		require.NoError(t, err)
		require.Equal(t, 1, countLiveSessions(t, pool))
		h.Close()
		require.Equal(t, 1, countLiveSessions(t, pool), "shutdown must not delete session rows")
	})
}
