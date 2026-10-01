package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

func newH2CChild(h http.Handler) *httptest.Server {
	return startCleartextHTTP2(h)
}

func startCleartextHTTP2(h http.Handler) *httptest.Server {
	srv := httptest.NewUnstartedServer(h)
	if err := ConfigureCleartextHTTP2(srv.Config); err != nil {
		panic(err)
	}
	srv.Start()
	return srv
}

func TestH2CServerAdvertisesStreamCap(t *testing.T) {
	s := H2CServer()
	if s.MaxConcurrentStreams != DefaultH2MaxConcurrentStreams {
		t.Fatalf("MaxConcurrentStreams = %d, want %d", s.MaxConcurrentStreams, DefaultH2MaxConcurrentStreams)
	}
	if s.MaxConcurrentStreams != 4096 {
		t.Fatalf("MaxConcurrentStreams = %d, want 4096", s.MaxConcurrentStreams)
	}
	if s.MaxConcurrentStreams == 256 {
		t.Fatal("SETTINGS must not equal the per-peer interceptor cap")
	}
	if s.MaxConcurrentStreams == 0 {
		t.Fatal("zero would hide SETTINGS_MAX_CONCURRENT_STREAMS")
	}
}

func TestProxy_HTTP1_JSONChatAndStatsShards(t *testing.T) {
	chat := newH2CChild(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("chat path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"chat.completion"}`)
	}))
	t.Cleanup(chat.Close)
	shards := newH2CChild(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stats/shards" {
			t.Errorf("shards path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"shards":[]}`)
	}))
	t.Cleanup(shards.Close)

	chatSrv := httptest.NewServer(Handler(newRoutes(map[string]string{
		"v1": strings.TrimPrefix(chat.URL, "http://"),
	})))
	t.Cleanup(chatSrv.Close)
	shardsSrv := httptest.NewServer(Handler(newRoutes(map[string]string{
		"v9.1.0": strings.TrimPrefix(shards.URL, "http://"),
	})))
	t.Cleanup(shardsSrv.Close)

	chatResp, err := http.Get(chatSrv.URL + "/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = chatResp.Body.Close() })
	if chatResp.ProtoMajor != 1 {
		t.Fatalf("chat proto major = %d, want 1", chatResp.ProtoMajor)
	}
	if chatResp.StatusCode != http.StatusOK {
		t.Fatalf("chat status = %d", chatResp.StatusCode)
	}
	body, _ := io.ReadAll(chatResp.Body)
	if string(body) != `{"object":"chat.completion"}` {
		t.Fatalf("chat body = %q", body)
	}

	shardsResp, err := http.Get(shardsSrv.URL + "/stats/shards")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shardsResp.Body.Close() })
	if shardsResp.ProtoMajor != 1 {
		t.Fatalf("stats/shards proto major = %d, want 1", shardsResp.ProtoMajor)
	}
	if shardsResp.StatusCode != http.StatusOK {
		t.Fatalf("stats/shards status = %d", shardsResp.StatusCode)
	}
	got, _ := io.ReadAll(shardsResp.Body)
	if string(got) != `{"shards":[]}` {
		t.Fatalf("stats/shards body = %q", got)
	}
}

func TestProxy_H2C_RPC_OneParentConnNStreams(t *testing.T) {
	assertProxyH2COverlappingStreamsShareOneTCP(t, 8)
}

func TestProxy_H2C_RPC_MoreThan100StreamsShareOneTCP(t *testing.T) {
	// HAProxy default SETTINGS is 100. Past that, golang dials another TCP
	// on both the public hop and versiond→child unless SETTINGS is 4096.
	assertProxyH2COverlappingStreamsShareOneTCP(t, 101)
}

func assertProxyH2COverlappingStreamsShareOneTCP(t *testing.T, n int) {
	t.Helper()
	var sawProto string
	var protoMu sync.Mutex
	started := make(chan struct{}, n)
	release := make(chan struct{})
	child := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protoMu.Lock()
		sawProto = r.Proto
		protoMu.Unlock()
		// Warmup returns immediately so both hops apply SETTINGS before
		// the burst. Holding it would leave the client on the library
		// default of 100 streams.
		if r.URL.Path == "/rpc/warmup" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path != "/rpc/" {
			t.Errorf("child path = %s", r.URL.Path)
		}
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	var childConns atomic.Int32
	child.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			childConns.Add(1)
		}
	}
	if err := ConfigureCleartextHTTP2(child.Config); err != nil {
		t.Fatal(err)
	}
	child.Start()
	t.Cleanup(child.Close)

	parent := startCleartextHTTP2(Handler(newRoutes(map[string]string{
		"v1": strings.TrimPrefix(child.URL, "http://"),
	})))
	t.Cleanup(parent.Close)

	var parentDials atomic.Int32
	client := newH2CClient(t, func() { parentDials.Add(1) })
	// golang.org/x/net/http2 uses initialMaxConcurrentStreams=100 until
	// SETTINGS arrives. One request first, or a cold burst of 101 dials
	// a second TCP. That window is TestH2CClient_BurstBeforeSettingsDialsAnotherTCP.
	warm, err := client.Get(parent.URL + "/v1/rpc/warmup")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, warm.Body)
	_ = warm.Body.Close()
	if warm.StatusCode != http.StatusNoContent {
		t.Fatalf("warmup status = %d", warm.StatusCode)
	}
	if got := parentDials.Load(); got != 1 {
		t.Fatalf("warmup client→versiond dials = %d, want 1", got)
	}
	if got := childConns.Load(); got != 1 {
		t.Fatalf("warmup versiond→child connections = %d, want 1", got)
	}
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(parent.URL + "/v1/rpc/")
			if err != nil {
				errCh <- err
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				errCh <- fmt.Errorf("status %d", resp.StatusCode)
				return
			}
			if resp.ProtoMajor != 2 {
				errCh <- fmt.Errorf("client proto %s", resp.Proto)
			}
		}()
	}
	func() {
		defer close(release)
		for i := 0; i < n; i++ {
			select {
			case <-started:
			case <-time.After(10 * time.Second):
				t.Fatalf("%d/%d streams started; HTTP/2 multiplexing is off or SETTINGS too low", i, n)
			}
		}
		if got := parentDials.Load(); got != 1 {
			t.Fatalf("client→versiond dials = %d, want 1", got)
		}
		if got := childConns.Load(); got != 1 {
			t.Fatalf("versiond→child connections = %d, want 1", got)
		}
		protoMu.Lock()
		if sawProto != "HTTP/2.0" {
			t.Fatalf("child proto = %q, want HTTP/2.0", sawProto)
		}
		protoMu.Unlock()
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestProxy_RPCStats_MergeStillWorks(t *testing.T) {
	okBody, _ := json.Marshal(rpcStatsSnapshot{
		HostAddress: "gonka1host",
		MinuteUnix:  1710000000,
		Host:        rpcStatsHost{Requests: 4, Zones: []rpcStatsZone{}},
		Shards:      []rpcStatsShard{{EscrowID: "1", Requests: 4}},
	})
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stats/rpc" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(okBody)
	}))
	t.Cleanup(ok.Close)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(down.Close)

	srv := httptest.NewServer(Handler(newRoutes(map[string]string{
		"v1": strings.TrimPrefix(down.URL, "http://"),
		"v2": strings.TrimPrefix(ok.URL, "http://"),
	})))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/devshard/stats/rpc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if resp.ProtoMajor != 1 {
		t.Fatalf("proto major = %d, want 1", resp.ProtoMajor)
	}
	var snap rpcStatsSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	if !snap.Partial {
		t.Fatalf("want partial merge, got %+v", snap)
	}
	if snap.Host.Requests != 4 {
		t.Fatalf("requests = %d", snap.Host.Requests)
	}
}

func TestH2C_WithoutH2CFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/rpc/")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.ProtoMajor != 1 {
		t.Fatalf("default client proto major = %d, want 1", resp.ProtoMajor)
	}

	client := newH2CClient(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/rpc/", nil)
	if err != nil {
		t.Fatal(err)
	}
	h2resp, err := client.Do(req)
	if err == nil {
		defer h2resp.Body.Close()
		t.Fatalf("h2c client succeeded Proto=%s status=%d; h2 without cleartext HTTP/2 must fail closed", h2resp.Proto, h2resp.StatusCode)
	}
}

func TestConfigureCleartextHTTP2_ShutdownWaitsForInFlightHTTP2(t *testing.T) {
	entered := make(chan struct{})
	const hold = 500 * time.Millisecond
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(entered)
			time.Sleep(hold)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("done"))
		}),
	}
	if err := ConfigureCleartextHTTP2(srv); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	resCh := make(chan struct {
		body string
		err  error
	}, 1)
	go func() {
		resp, err := newH2CClient(t, nil).Get("http://" + ln.Addr().String() + "/slow")
		if err != nil {
			resCh <- struct {
				body string
				err  error
			}{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		resCh <- struct {
			body string
			err  error
		}{body: string(b), err: err}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not start")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < hold-150*time.Millisecond {
		t.Fatalf("Shutdown returned in %s while the HTTP/2 handler was still running", time.Since(start))
	}
	res := <-resCh
	if res.err != nil {
		t.Fatal(res.err)
	}
	if res.body != "done" {
		t.Fatalf("body = %q, want done", res.body)
	}
	if err := <-errCh; err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

// TestH2CClient_BurstBeforeSettingsDialsAnotherTCP is the cold-burst
// failure: until SETTINGS, the client allows 100 streams and the 101st
// dials another TCP. The server here never writes a preface, so one run
// always hits that window.
func TestH2CClient_BurstBeforeSettingsDialsAnotherTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var dials atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			dials.Add(1)
			go func() { _, _ = io.Copy(io.Discard, c) }()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	tr := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, ln.Addr().String())
		},
	}
	client := &http.Client{Transport: tr}
	t.Cleanup(func() {
		cancel()
		tr.CloseIdleConnections()
		_ = ln.Close()
	})

	const n = 101
	for i := 0; i < n; i++ {
		go func() {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://h2c/rpc/", nil)
			if err != nil {
				return
			}
			resp, err := client.Do(req)
			if resp != nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
			_ = err
		}()
	}

	deadline := time.Now().Add(5 * time.Second)
	for dials.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	got := dials.Load()
	cancel()
	if got != 2 {
		t.Fatalf("dials before SETTINGS = %d, want 2 (stream 101 opens a second TCP while the cap is still 100)", got)
	}
}

func newH2CClient(t *testing.T, onDial func()) *http.Client {
	t.Helper()
	tr := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			if onDial != nil {
				onDial()
			}
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

func TestChildH2TransportPINGsIdleMux(t *testing.T) {
	tr := newChildH2Transport()
	if tr.ReadIdleTimeout != DefaultChildH2ReadIdleTimeout {
		t.Fatalf("ReadIdleTimeout = %s, want %s", tr.ReadIdleTimeout, DefaultChildH2ReadIdleTimeout)
	}
	if tr.PingTimeout != DefaultChildH2PingTimeout {
		t.Fatalf("PingTimeout = %s, want %s", tr.PingTimeout, DefaultChildH2PingTimeout)
	}
	if tr.IdleConnTimeout != DefaultChildH2IdleConnTimeout {
		t.Fatalf("IdleConnTimeout = %s, want %s", tr.IdleConnTimeout, DefaultChildH2IdleConnTimeout)
	}
	if tr.ReadIdleTimeout != 15*time.Second || tr.PingTimeout != 5*time.Second || tr.IdleConnTimeout != 120*time.Second {
		t.Fatal("must stay aligned with transport.DefaultRPCH2ReadIdleTimeout / PingTimeout / IdleConnTimeout")
	}
}

func TestProxy_ChildH2HalfOpenFailsWithinPing(t *testing.T) {
	child := newH2CChild(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(child.Close)
	backend, err := url.Parse(child.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, hold := startTCPHoldProxy(t, backend.Host)

	readIdle := 200 * time.Millisecond
	ping := 200 * time.Millisecond
	tr := newChildH2Transport()
	tr.ReadIdleTimeout = readIdle
	tr.PingTimeout = ping
	old := childTransport
	childTransport = tr
	t.Cleanup(func() {
		childTransport = old
		tr.CloseIdleConnections()
	})

	srv := httptest.NewServer(Handler(newRoutes(map[string]string{
		"v1": strings.TrimPrefix(proxyURL, "http://"),
	})))
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL + "/v1/rpc/")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("warmup status = %d", resp.StatusCode)
	}

	hold()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/rpc/", nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	resp, err = srv.Client().Do(req)
	elapsed := time.Since(start)
	if resp != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("half-open child mux hung %s; ReadIdle+Ping should fail the reverse proxy", elapsed)
	}
	if err == nil && resp != nil && resp.StatusCode == http.StatusNoContent {
		t.Fatal("half-open child mux still served; want transport error or 502")
	}
}

func startTCPHoldProxy(t *testing.T, backendHost string) (proxyURL string, hold func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var mu sync.Mutex
	var backends []net.Conn
	var frozen atomic.Bool
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if frozen.Load() {
				continue
			}
			b, err := net.Dial("tcp", backendHost)
			if err != nil {
				_ = c.Close()
				continue
			}
			mu.Lock()
			backends = append(backends, b)
			mu.Unlock()
			go func(c, b net.Conn) {
				go func() { _, _ = io.Copy(b, c) }()
				_, _ = io.Copy(c, b)
			}(c, b)
		}
	}()
	hold = func() {
		frozen.Store(true)
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, b := range backends {
			_ = b.Close()
		}
	}
	return "http://" + ln.Addr().String(), hold
}
