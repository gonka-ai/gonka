package user

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/transport"
)

func TestWaitRouterCatalog_InProcessSkips(t *testing.T) {
	var height uint64 = 100
	session := setupHeartbeatSession(t, &height)
	t.Cleanup(func() { _ = session.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.NoError(t, session.WaitRouterCatalog(ctx))
}

func TestWaitRouterCatalog_WaitsUntil200(t *testing.T) {
	var ready atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/healthz" {
			http.NotFound(w, r)
			return
		}
		if !ready.Load() {
			http.Error(w, "undeclared", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := transport.DefaultClientConfig()
	cfg.RoutePrefix = "/devshard/v2"
	client := transport.NewHTTPClient(srv.URL, "1", nil, cfg)
	session := &Session{clients: []HostClient{client}, escrowID: "1"}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- session.WaitRouterCatalog(ctx) }()
	time.Sleep(50 * time.Millisecond)
	ready.Store(true)
	require.NoError(t, <-errCh)
}

func TestHeartbeatLoop_WaitsForCatalogBeforeFirstTick(t *testing.T) {
	var height uint64 = 100
	session := setupHeartbeatSessionWithOracles(t, &height, nil,
		WithHeartbeatConfig(heightsync.HeartbeatConfig{Interval: 40 * time.Millisecond}))
	t.Cleanup(func() { _ = session.Close() })

	var ready atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/healthz" {
			http.NotFound(w, r)
			return
		}
		if !ready.Load() {
			http.Error(w, "undeclared", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := transport.DefaultClientConfig()
	cfg.RoutePrefix = "/devshard/v2"
	httpClient := transport.NewHTTPClient(srv.URL, "1", nil, cfg)
	session.mu.Lock()
	session.clients = append(session.clients, httpClient)
	session.mu.Unlock()

	session.StartHeartbeatLoop()
	time.Sleep(150 * time.Millisecond)
	require.Zero(t, session.Nonce(), "heartbeat must not tick before catalog admission")

	ready.Store(true)
	require.Eventually(t, func() bool {
		return session.Nonce() >= 1
	}, 3*time.Second, 20*time.Millisecond, "heartbeat must start after catalog admission")
}

func TestWaitRouterCatalog_404IsNotAdmission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/healthz":
			w.WriteHeader(http.StatusOK)
			return
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := transport.DefaultClientConfig()
	cfg.RoutePrefix = "/devshard/v2"
	client := transport.NewHTTPClient(srv.URL, "1", nil, cfg)
	session := &Session{clients: []HostClient{client}, escrowID: "1"}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, session.WaitRouterCatalog(ctx), context.DeadlineExceeded,
		"root /health and /healthz must not count as /{version}/healthz")
}

func TestWaitRouterCatalog_Catalog503KeepsWaiting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/healthz" {
			http.Error(w, "undeclared", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := transport.DefaultClientConfig()
	cfg.RoutePrefix = "/devshard/v2"
	client := transport.NewHTTPClient(srv.URL, "1", nil, cfg)
	session := &Session{clients: []HostClient{client}, escrowID: "1"}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, session.WaitRouterCatalog(ctx), context.DeadlineExceeded,
		"router process /healthz must not count as catalog admission")
}

func TestWaitRouterCatalog_Canceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "undeclared", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	cfg := transport.DefaultClientConfig()
	cfg.RoutePrefix = "/devshard/v2"
	client := transport.NewHTTPClient(srv.URL, "1", nil, cfg)
	session := &Session{clients: []HostClient{client}, escrowID: "1"}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, session.WaitRouterCatalog(ctx), context.DeadlineExceeded)
}
