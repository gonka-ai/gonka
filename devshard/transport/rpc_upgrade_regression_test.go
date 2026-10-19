package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"common/httpguard"
	"devshard/signing"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func TestRPCUpgradeActualHTTP1Response(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, e := ln.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		b := make([]byte, 24)
		_, _ = io.ReadFull(c, b)
		_, _ = io.WriteString(c, "HTTP/1.1 400 Bad Request\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
		// Drain concurrent SETTINGS/HEADERS so close does not replace our H1
		// response with a TCP reset caused by unread incoming bytes.
		_ = c.(*net.TCPConn).CloseWrite()
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		_, _ = io.Copy(io.Discard, c)
	}()
	tr := newRPCH2Transport(nil, true, "", 0, 0, 0)
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://"+ln.Addr().String()+"/Attach", strings.NewReader("body"))
	_, err = tr.RoundTrip(req)
	if err == nil {
		t.Fatal("wanted error")
	}
	t.Logf("HTTP/1 response to H2 preface: %v", err)
	// The HTTP/2 read loop may report the H1 frame header, or connection
	// establishment may notice that loop has already closed. Both must allow
	// fallback; only explicit protocol evidence earns the long cache TTL.
	if !isRPCH2Miss(err) {
		t.Fatalf("HTTP/1-only origin must allow fallback: %v", err)
	}
}

func TestRPCUpgradeTLSNonOverlap(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = &tls.Config{NextProtos: []string{"h2", "http/1.1"}}
	srv.StartTLS()
	defer srv.Close()
	tr := newRPCH2Transport(nil, false, "", 0, 0, 0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := tr.DialTLSContext(ctx, "tcp", srv.Listener.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	if err == nil {
		t.Fatal("wanted mismatch")
	}
	if classifyRPCH2Miss(err) != h2ProtocolMiss {
		t.Fatalf("ALPN mismatch must be a protocol miss: %v", err)
	}
	if !strings.Contains(err.Error(), "no application protocol") {
		t.Fatalf("unexpected error %v", err)
	}
}

type upgradeAuthHandler struct{}

func (upgradeAuthHandler) Attach(context.Context, *connect.Request[rpcpb.AttachRequest]) (*connect.Response[rpcpb.AttachResponse], error) {
	return connect.NewResponse(&rpcpb.AttachResponse{SessionToken: []byte("refreshed-h1-token"), ExpiresAt: time.Now().Add(2 * time.Minute).Unix()}), nil
}
func (upgradeAuthHandler) Watch(context.Context, *connect.Request[rpcpb.WatchRequest], *connect.ServerStream[rpcpb.SessionEvent]) error {
	return nil
}

func TestRPCUpgradeRefreshH2RateLimitDoesNotRetryH1(t *testing.T) {
	ResetRPCH2MissCacheForTest()
	t.Cleanup(ResetRPCH2MissCacheForTest)
	ResetRPCH2ClientPoolForTest()
	t.Cleanup(ResetRPCH2ClientPoolForTest)
	httpguard.SetAllowPrivate(true)
	var h1, h2hits atomic.Int32
	_, handler := rpcpbconnect.NewPeerAuthServiceHandler(upgradeAuthHandler{})
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 {
			h2hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "300")
			w.WriteHeader(429)
			_, _ = io.WriteString(w, `{"code":"resource_exhausted","message":"rate capped"}`)
			return
		}
		h1.Add(1)
		handler.ServeHTTP(w, r)
	}), &http2.Server{}))
	defer server.Close()
	signer, e := signing.GenerateKey()
	if e != nil {
		t.Fatal(e)
	}
	pc := NewPeerConn(PeerConnConfig{BaseURL: server.URL, HostAddress: signer.Address(), Signer: signer, DirectMux: true, DialSet: PeerRPCDialSet{H2URL: server.URL}})
	defer pc.Close()
	pc.publishToken([]byte("existing-token"), time.Now().Add(2*time.Minute))
	pc.state.Store(stateReady)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err := pc.refreshLive(ctx)
	if err == nil {
		t.Fatal("H2 rate refusal must propagate")
	}
	if h2hits.Load() != 1 || h1.Load() != 0 || skipRPCH2(server.URL) {
		t.Fatalf("refusal retried or cached: h2=%d h1=%d", h2hits.Load(), h1.Load())
	}
}

func TestRPCUpgradeApplicationHTTP2TextIsNotTransportFailure(t *testing.T) {
	err := connect.NewWireError(connect.CodeUnavailable, errors.New("http2: upstream temporary outage"))
	t.Logf("wireError=%v code=%s currentH2Miss=%v", connect.IsWireError(err), connect.CodeOf(err), isRPCH2Miss(err))
	if isRPCH2Miss(err) {
		t.Fatal("application error must not change protocol")
	}
}

type upgradeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f upgradeRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRPCUpgradeSiblingCacheClearsH2Switch(t *testing.T) {
	ResetRPCH2MissCacheForTest()
	defer ResetRPCH2MissCacheForTest()
	ResetRPCH2ClientPoolForTest()
	defer ResetRPCH2ClientPoolForTest()
	httpguard.SetAllowPrivate(true)
	var h1hits, h2hits atomic.Int32
	_, handler := rpcpbconnect.NewPeerAuthServiceHandler(upgradeAuthHandler{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h1hits.Add(1); handler.ServeHTTP(w, r) }))
	defer server.Close()
	signer, e := signing.GenerateKey()
	if e != nil {
		t.Fatal(e)
	}
	pc := NewPeerConn(PeerConnConfig{BaseURL: server.URL, HostAddress: signer.Address(), Signer: signer, DirectMux: true, DoorEscrowID: "escrow-1", DialSet: PeerRPCDialSet{H2URL: server.URL}})
	defer pc.Close()
	pc.origin.h2 = upgradeRoundTripFunc(func(*http.Request) (*http.Response, error) { h2hits.Add(1); return nil, io.EOF })
	pc.origin.setH2(true)         // previous successful h2 session; token already cleared by loop
	rememberRPCH2Miss(server.URL) // sibling has observed protocol miss
	_, _, err := pc.attach()
	if err != nil {
		t.Fatal(err)
	}
	if pc.origin.usingH2() || h2hits.Load() != 0 || h1hits.Load() != 1 {
		t.Fatalf("sibling miss ignored: H1=%d H2=%d", h1hits.Load(), h2hits.Load())
	}
}

type upgradeLiveWatchHandler struct{ closed atomic.Int32 }

func (h *upgradeLiveWatchHandler) Attach(context.Context, *connect.Request[rpcpb.AttachRequest]) (*connect.Response[rpcpb.AttachResponse], error) {
	return connect.NewResponse(&rpcpb.AttachResponse{SessionToken: []byte("live-token"), ExpiresAt: time.Now().Add(2 * time.Minute).Unix()}), nil
}
func (h *upgradeLiveWatchHandler) Watch(ctx context.Context, _ *connect.Request[rpcpb.WatchRequest], stream *connect.ServerStream[rpcpb.SessionEvent]) error {
	defer h.closed.Add(1)
	if err := stream.Send(&rpcpb.SessionEvent{Event: &rpcpb.SessionEvent_Beat{Beat: &rpcpb.Heartbeat{UnixSeconds: time.Now().Unix()}}}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}
func TestRPCUpgradeH1RefreshEOFPreservesLiveWatch(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	var requests atomic.Int32
	observed := make(chan string, 1)
	handlerImpl := new(upgradeLiveWatchHandler)
	_, handler := rpcpbconnect.NewPeerAuthServiceHandler(handlerImpl)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Attach") {
			n := requests.Add(1)
			if n == 2 {
				_, _ = io.Copy(io.Discard, r.Body)
				_ = r.Body.Close()
				c, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = c.Close()
				}
				return
			}
			if n == 3 {
				observed <- r.Header.Get(SessionHeader)
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	signer, e := signing.GenerateKey()
	if e != nil {
		t.Fatal(e)
	}
	pc := NewPeerConn(PeerConnConfig{BaseURL: server.URL, HostAddress: signer.Address(), Signer: signer, DirectMux: true, DoorEscrowID: "escrow-1", BackoffMin: time.Millisecond, BackoffMax: time.Millisecond, Jitter: func(d time.Duration) time.Duration {
		if d > time.Second {
			return 40 * time.Millisecond
		}
		return d
	}})
	defer pc.Close()
	pc.Start()
	select {
	case header := <-observed:
		dropped := header == ""
		t.Logf("H2 disabled=%v thirdAttachSessionHeader=%q anonymous=%v watchesClosedBeforeThirdAttach=%d", pc.cfg.DialSet.H2URL == "", header, dropped, handlerImpl.closed.Load())
		expected := false
		if dropped != expected {
			t.Fatalf("session dropped=%v want=%v", dropped, expected)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no third attach")
	}
}

func TestRPCUpgradeConnectClosureFallsBack(t *testing.T) {
	for _, transportErr := range []error{
		errors.New("http2: client conn could not be established"),
		http2.GoAwayError{LastStreamID: 3, ErrCode: http2.ErrCodeNo, DebugData: "shutdown"},
	} {
		t.Run(transportErr.Error(), func(t *testing.T) {
			ResetRPCH2MissCacheForTest()
			t.Cleanup(ResetRPCH2MissCacheForTest)
			httpguard.SetAllowPrivate(true)
			var h1hits atomic.Int32
			_, handler := rpcpbconnect.NewPeerAuthServiceHandler(upgradeAuthHandler{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				h1hits.Add(1)
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			signer, err := signing.GenerateKey()
			if err != nil {
				t.Fatal(err)
			}
			pc := NewPeerConn(PeerConnConfig{BaseURL: server.URL, HostAddress: signer.Address(), Signer: signer, DirectMux: true, DoorEscrowID: "escrow-1", DialSet: PeerRPCDialSet{H2URL: server.URL}})
			defer pc.Close()
			pc.origin.h2 = upgradeRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, transportErr })
			_, _, err = pc.attach()
			if err != nil {
				t.Fatal(err)
			}
			if h1hits.Load() != 1 || pc.origin.usingH2() {
				t.Fatal("Connect-wrapped closure did not fall back")
			}
			rpch2Miss.mu.Lock()
			entry := rpch2Miss.entries[inferenceHostKey(server.URL)]
			rpch2Miss.mu.Unlock()
			if entry.backoff != DefaultRPCH2CloseBackoff {
				t.Fatalf("wanted short backoff, got %v", entry.backoff)
			}
		})
	}
}

func TestRPCUpgradeSiblingProbeLeavesH1RenewalBudget(t *testing.T) {
	ResetRPCH2MissCacheForTest()
	t.Cleanup(ResetRPCH2MissCacheForTest)
	httpguard.SetAllowPrivate(true)
	var h1hits atomic.Int32
	_, handler := rpcpbconnect.NewPeerAuthServiceHandler(upgradeAuthHandler{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h1hits.Add(1)
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	signer, err := signing.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	pc := NewPeerConn(PeerConnConfig{BaseURL: server.URL, HostAddress: signer.Address(), Signer: signer, DirectMux: true, DialSet: PeerRPCDialSet{H2URL: server.URL}})
	defer pc.Close()
	pc.publishToken([]byte("existing-token"), time.Now().Add(2*time.Minute))
	pc.state.Store(stateReady)
	// Hold a sibling's lease throughout renewal. Success must not depend on
	// that sibling completing; H1 has a reserved half of the deadline.
	key := inferenceHostKey(server.URL)
	generation, acquired := rpch2Miss.begin(key)
	if !acquired {
		t.Fatal("failed to start sibling probe")
	}
	defer rpch2Miss.finish(key, generation, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err = pc.refreshLive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h1hits.Load() != 1 || pc.origin.usingH2() {
		t.Fatal("healthy H1 session was not renewed while sibling was probing")
	}
}
