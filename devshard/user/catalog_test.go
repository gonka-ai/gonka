package user

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
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
		if r.URL.Path != "/devshard/v2/healthz" {
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

func TestWaitRouterCatalog_ThroughPublicProxy(t *testing.T) {
	var ready atomic.Bool
	var catalogRequests atomic.Int32
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/healthz" {
			http.NotFound(w, r)
			return
		}
		catalogRequests.Add(1)
		if !ready.Load() {
			http.Error(w, "undeclared", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(router.Close)
	target, err := url.Parse(router.URL)
	require.NoError(t, err)
	// Match the public nginx contract: only /devshard/ reaches the router,
	// and the prefix is removed before forwarding. Bare /v2/healthz is 404.
	publicMux := http.NewServeMux()
	publicMux.Handle("/devshard/", http.StripPrefix("/devshard", httputil.NewSingleHostReverseProxy(target)))
	public := httptest.NewServer(publicMux)
	t.Cleanup(public.Close)

	cfg := transport.DefaultClientConfig()
	cfg.RoutePrefix = "/devshard/v2"
	client := transport.NewHTTPClient(public.URL, "1", nil, cfg)
	session := &Session{clients: []HostClient{client}, escrowID: "1"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- session.WaitRouterCatalog(ctx) }()
	require.Eventually(t, func() bool { return catalogRequests.Load() > 0 }, time.Second, 10*time.Millisecond,
		"gateway catalog probe must reach the router through the public prefix")
	select {
	case err := <-errCh:
		t.Fatalf("catalog wait returned before the router admitted the version: %v", err)
	default:
	}
	ready.Store(true)
	require.NoError(t, <-errCh)
}

func TestHeartbeatLoop_WaitsForCatalogBeforeFirstTick(t *testing.T) {
	var height uint64 = 100
	session := setupBlindHeartbeatSession(t, &height,
		WithHeartbeatConfig(heightsync.HeartbeatConfig{Interval: 40 * time.Millisecond}))
	t.Cleanup(func() { _ = session.Close() })
	// The fixture's bootstrap inference already moved the log, so "did not tick"
	// is measured from there rather than from nonce 0.
	base := session.Nonce()

	var ready atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/devshard/v2/healthz" {
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
	require.Equal(t, base, session.Nonce(), "heartbeat must not tick before catalog admission")

	ready.Store(true)
	require.Eventually(t, func() bool {
		return session.Nonce() > base
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
		"root /health and /healthz must not count as /devshard/{version}/healthz")
}

func TestWaitRouterCatalog_Catalog503KeepsWaiting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/devshard/v2/healthz" {
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

func TestWaitRouterCatalog_LateResolvedHostAdmits(t *testing.T) {
	var staleProbes atomic.Int32
	stale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/devshard/v2/healthz" {
			http.NotFound(w, r)
			return
		}
		staleProbes.Add(1)
		http.Error(w, "undeclared", http.StatusServiceUnavailable)
	}))
	t.Cleanup(stale.Close)
	var readyProbes atomic.Int32
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/devshard/v2/healthz" {
			http.NotFound(w, r)
			return
		}
		readyProbes.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ready.Close)

	cfg := transport.DefaultClientConfig()
	cfg.RoutePrefix = "/devshard/v2"
	gate := make(chan struct{})
	lazy := transport.NewLazy(func() (HostClient, error) {
		<-gate
		return transport.NewHTTPClient(ready.URL, "1", nil, cfg), nil
	}, releaseHostClient, 0, 0)
	t.Cleanup(func() { lazy.Close() })
	lazy.Start()
	session := &Session{clients: []HostClient{
		transport.NewHTTPClient(stale.URL, "1", nil, cfg),
		newLazyHostClient(lazy),
	}, escrowID: "1"}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.WaitRouterCatalog(ctx) }()
	require.Eventually(t, func() bool { return staleProbes.Load() > 0 }, time.Second, time.Millisecond)
	close(gate)
	require.Eventually(t, func() bool { _, ok := lazy.Peek(); return ok }, time.Second, time.Millisecond)

	require.NoError(t, <-done, "catalog must admit the host whose URL resolved after the first probe")
	require.Greater(t, readyProbes.Load(), int32(0))
}

func TestWaitRouterCatalog_RetriesFailedLookup(t *testing.T) {
	var readyProbes atomic.Int32
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/devshard/v2/healthz" {
			http.NotFound(w, r)
			return
		}
		readyProbes.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ready.Close)

	cfg := transport.DefaultClientConfig()
	cfg.RoutePrefix = "/devshard/v2"
	var lookups atomic.Int32
	lazy := transport.NewLazy(func() (HostClient, error) {
		if lookups.Add(1) == 1 {
			return nil, errors.New("host info failed")
		}
		return transport.NewHTTPClient(ready.URL, "1", nil, cfg), nil
	}, releaseHostClient, time.Millisecond, time.Millisecond)
	t.Cleanup(func() { lazy.Close() })
	session := &Session{clients: []HostClient{newLazyHostClient(lazy)}, escrowID: "1"}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	require.NoError(t, session.WaitRouterCatalog(ctx))
	require.GreaterOrEqual(t, lookups.Load(), int32(2), "a failed lookup must run again before the catalog gives up")
	require.Greater(t, readyProbes.Load(), int32(0))
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
