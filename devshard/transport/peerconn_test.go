package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"common/httpguard"
	devtest "devshard/internal/testutil"
	"devshard/observability"
)

func TestPeerConnConfig_WatchUsesHostEscrow(t *testing.T) {
	cfg := PeerConnConfig{
		BaseURL:      "http://peer.example",
		RoutePrefix:  "/devshard/dev",
		DoorEscrowID: "9801",
	}
	require.Contains(t, cfg.connectBase(cfg.DoorEscrowID), "/sessions/9801/rpc")
	require.Contains(t, cfg.connectBase(HostRPCEscrowID), "/sessions/_/rpc")
	require.NotEqual(t, cfg.connectBase(cfg.DoorEscrowID), cfg.connectBase(HostRPCEscrowID))
}

func TestPeerConn_RefreshDelay(t *testing.T) {
	now := time.Unix(1_000, 0)
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:     "http://127.0.0.1:1",
		HostAddress: "gonka1delay",
		DirectMux:   true,
		BackoffMin:  50 * time.Millisecond,
		Now:         func() time.Time { return now },
	})
	t.Cleanup(pc.Close)
	require.Equal(t, 50*time.Millisecond, pc.refreshDelay(now), "expired token must not tight-loop")
	require.Equal(t, 50*time.Millisecond, pc.refreshDelay(now.Add(-time.Second)))
	require.Equal(t, 3*time.Second, pc.refreshDelay(now.Add(4*time.Second)))
	require.Equal(t, 50*time.Millisecond, pc.refreshDelay(now.Add(40*time.Millisecond)))
}

func TestPeerConn_NextRefreshWaitJitters(t *testing.T) {
	now := time.Unix(1_000, 0)
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:     "http://127.0.0.1:1",
		HostAddress: "gonka1jitter",
		DirectMux:   true,
		BackoffMin:  50 * time.Millisecond,
		Now:         func() time.Time { return now },
		Jitter:      func(d time.Duration) time.Duration { return d / 2 },
	})
	t.Cleanup(pc.Close)
	base := pc.refreshDelay(now.Add(4 * time.Second))
	require.Equal(t, 3*time.Second, base)
	require.Equal(t, base/2, pc.nextRefreshWait(now.Add(4*time.Second), 0))
	require.Equal(t, 25*time.Millisecond, pc.nextRefreshWait(now.Add(4*time.Second), 50*time.Millisecond))

	pc.cfg.Jitter = nil
	var lo, hi time.Duration = time.Hour, 0
	for i := 0; i < 64; i++ {
		w := pc.nextRefreshWait(now.Add(4*time.Second), 0)
		require.GreaterOrEqual(t, w, base/2)
		require.LessOrEqual(t, w, base)
		if w < lo {
			lo = w
		}
		if w > hi {
			hi = w
		}
	}
	require.Less(t, lo, hi, "refresh waits must spread in [d/2, d]")
}

func TestPeerConn_MetricsAreVersioned(t *testing.T) {
	host := "gonka1metricver"
	v5 := NewPeerConn(PeerConnConfig{
		BaseURL:     "http://127.0.0.1:1",
		HostAddress: host,
		RoutePrefix: "/devshard/v5",
	})
	t.Cleanup(v5.Close)
	v6 := NewPeerConn(PeerConnConfig{
		BaseURL:     "http://127.0.0.1:1",
		HostAddress: host,
		RoutePrefix: "/devshard/v6",
	})
	t.Cleanup(v6.Close)

	require.Equal(t, 1.0, testutil.ToFloat64(observability.PeerSessionStateGauge(host+"@v5", observability.PeerSessionUnauthenticated)))
	require.Equal(t, 1.0, testutil.ToFloat64(observability.PeerSessionStateGauge(host+"@v6", observability.PeerSessionUnauthenticated)))
	require.Equal(t, 0.0, testutil.ToFloat64(observability.PeerSessionStateGauge(host, observability.PeerSessionUnauthenticated)),
		"bare address must not share a series with children")

	v5.Close()
	require.Equal(t, 1.0, testutil.ToFloat64(observability.PeerSessionStateGauge(host+"@v6", observability.PeerSessionUnauthenticated)),
		"closing one child must not ClearPeerSessionState the other")
}

func TestPeerConn_FailFast(t *testing.T) {
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:     "http://127.0.0.1:1",
		HostAddress: "gonka1nobody",
		DirectMux:   true,
	})
	t.Cleanup(pc.Close)
	httpClient := NewHTTPClient("http://127.0.0.1:1", "escrow-1", devtest.MustGenerateKey(t))
	rpc := NewRPCClient(httpClient, pc, ParseRPCEndpoints(EndpointSignatures))
	start := time.Now()
	_, err := rpc.GetSignatures(context.Background(), 1)
	elapsed := time.Since(start)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPeerNotReady)
	require.Less(t, elapsed, 200*time.Millisecond, "unauthenticated RPC must not wait on Attach")
}

func TestPeerConn_BackoffShape(t *testing.T) {
	peer := devtest.MustGenerateKey(t)
	var sleeps []time.Duration
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var n atomic.Int32
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:     "http://127.0.0.1:1",
		HostAddress: "gonka1backoff",
		Signer:      peer,
		DirectMux:   true,
		BackoffMin:  50 * time.Millisecond,
		BackoffMax:  5 * time.Second,
		Jitter:      func(d time.Duration) time.Duration { return d },
		Sleep: func(_ context.Context, d time.Duration) error {
			if d == 0 {
				return ctx.Err()
			}
			mu.Lock()
			sleeps = append(sleeps, d)
			mu.Unlock()
			if n.Add(1) >= 6 {
				cancel()
				return context.Canceled
			}
			return ctx.Err()
		},
	})
	t.Cleanup(pc.Close)
	pc.Start()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(sleeps) >= 4
	}, 3*time.Second, 5*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 50*time.Millisecond, sleeps[0])
	require.Equal(t, 100*time.Millisecond, sleeps[1])
	require.Equal(t, 200*time.Millisecond, sleeps[2])
	require.Equal(t, 400*time.Millisecond, sleeps[3])
	for _, d := range sleeps {
		require.LessOrEqual(t, d, 5*time.Second)
	}
}

func TestPeerConn_SSRF(t *testing.T) {
	httpguard.SetAllowPrivate(false)
	t.Cleanup(func() { httpguard.SetAllowPrivate(true) })

	peer := devtest.MustGenerateKey(t)
	pc := NewPeerConn(PeerConnConfig{
		BaseURL:     "http://127.0.0.1:1",
		HostAddress: "gonka1ssrf",
		Signer:      peer,
		DirectMux:   true,
		BackoffMax:  50 * time.Millisecond,
		BackoffMin:  10 * time.Millisecond,
	})
	t.Cleanup(pc.Close)
	pc.Start()
	require.Never(t, pc.Ready, 200*time.Millisecond, 20*time.Millisecond)
}

func TestSelectTransport_EmptyIsHTTPClient(t *testing.T) {
	httpClient := NewHTTPClient("http://127.0.0.1:1", "escrow-1", devtest.MustGenerateKey(t))
	got := SelectTransport(httpClient, "gonka1host", EndpointSet{}, nil)
	require.Equal(t, httpClient, got)
}

func TestSelectTransport_UnwiredKeepsHTTPClient(t *testing.T) {
	httpClient := NewHTTPClient("http://127.0.0.1:1", "escrow-1", devtest.MustGenerateKey(t))
	for _, raw := range []string{EndpointChat, EndpointGossip, "typo", "chat,diffs"} {
		t.Run(raw, func(t *testing.T) {
			got := SelectTransport(httpClient, "gonka1unwired", ParseRPCEndpoints(raw), nil)
			require.Equal(t, httpClient, got, "unwired names must not start Attach")
			require.False(t, PeerConnRegistered("gonka1unwired", peerConnConfigFromClient(httpClient, "gonka1unwired", nil).version()))
		})
	}
}

func TestSelectTransport_NamedEndpointUsesRPCClient(t *testing.T) {
	httpClient := NewHTTPClient("http://127.0.0.1:1", "escrow-1", devtest.MustGenerateKey(t))
	got := SelectTransport(httpClient, "gonka1host", ParseRPCEndpoints(EndpointSignatures), nil)
	rpc, ok := got.(*RPCClient)
	require.True(t, ok)
	t.Cleanup(rpc.Close)
	require.True(t, rpc.Uses(EndpointSignatures))
	require.False(t, rpc.Uses(EndpointGossip))
}

func TestSelectTransport_ChatWithSignaturesStillRPC(t *testing.T) {
	httpClient := NewHTTPClient("http://127.0.0.1:1", "escrow-1", devtest.MustGenerateKey(t))
	got := SelectTransport(httpClient, "gonka1hostchat", ParseRPCEndpoints(EndpointChat+","+EndpointSignatures), nil)
	rpc, ok := got.(*RPCClient)
	require.True(t, ok)
	t.Cleanup(rpc.Close)
	require.True(t, rpc.Uses(EndpointSignatures))
	require.True(t, rpc.Uses(EndpointChat), "unknown-to-Connect names stay in the set")
	require.True(t, ParseRPCEndpoints(EndpointChat).Has(EndpointChat))
	require.False(t, ParseRPCEndpoints(EndpointChat).NeedsAttach())
}

func TestSelectTransport_EmptyHostAddressKeepsHTTP(t *testing.T) {
	signer := devtest.MustGenerateKey(t)
	a := NewHTTPClient("http://peer-a.example", "escrow-1", signer)
	b := NewHTTPClient("http://peer-b.example", "escrow-1", signer)
	set := ParseRPCEndpoints(EndpointSignatures)
	gotA := SelectTransport(a, "", set, nil)
	gotB := SelectTransport(b, "  ", set, nil)
	require.Same(t, a, gotA)
	require.Same(t, b, gotB)
	require.NotSame(t, gotA, gotB)

	ver := peerConnConfigFromClient(a, "", nil).version()
	peerConnMu.Lock()
	_, collapsed := peerConnRegistry["@"+ver]
	peerConnMu.Unlock()
	require.False(t, collapsed, "empty HostAddress must not share a PeerConn")
}

func TestPoolWatchRoundTripper_IncrementsExhausted(t *testing.T) {
	peer := "gonka1pool"
	before := testutil.ToFloat64(observability.PeerPoolExhaustedCounter(peer))
	rt := &poolWatchRoundTripper{
		base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
		}),
		peer: peer,
		max:  1,
	}
	rt.inflight.Store(1)
	_, err := rt.RoundTrip(httptest.NewRequest(http.MethodGet, "http://example.invalid/", nil))
	require.NoError(t, err)
	require.Equal(t, 1.0, testutil.ToFloat64(observability.PeerPoolExhaustedCounter(peer))-before)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestParseRPCEndpoints(t *testing.T) {
	require.True(t, ParseRPCEndpoints("").Empty())
	set := ParseRPCEndpoints("gossip, diffs")
	require.True(t, set.Has(EndpointGossip))
	require.True(t, set.Has(EndpointDiffs))
	require.False(t, set.Has(EndpointSignatures))
}

func TestIsRetryableNonInference_ConnectCodes(t *testing.T) {
	require.False(t, IsRetryableNonInference(connect.NewError(connect.CodeUnauthenticated, errors.New("handshake required"))))
	require.True(t, IsRetryableNonInference(connect.NewError(connect.CodeResourceExhausted, errors.New("busy"))))
	require.True(t, IsRetryableNonInference(connect.NewError(connect.CodeUnavailable, errors.New("host initializing"))))
	require.False(t, IsRetryableNonInference(context.Canceled))
	require.False(t, IsRetryableNonInference(ErrPeerNotReady))
	require.False(t, IsRetryableNonInference(connect.NewError(connect.CodeInvalidArgument, errors.New("bad"))))
}

func TestRPCRetry_UnauthenticatedOnce(t *testing.T) {
	var n atomic.Int32
	start := time.Now()
	err := rpcRetry(context.Background(), func() error {
		n.Add(1)
		return connect.NewError(connect.CodeUnauthenticated, errors.New("stale"))
	})
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	require.Equal(t, int32(2), n.Load(), "one retry for a token race, then fail")
	require.Less(t, time.Since(start), time.Second, "must not spend the 5s non-inference budget")
}

func TestRPCRetry_UnauthenticatedThenOK(t *testing.T) {
	var n atomic.Int32
	err := rpcRetry(context.Background(), func() error {
		if n.Add(1) == 1 {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("stale"))
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, int32(2), n.Load())
}

func TestPeerConn_ReleaseSharedKeepsRegistry(t *testing.T) {
	peer := devtest.MustGenerateKey(t)
	host := "gonka1relshare"
	cfg := PeerConnConfig{
		BaseURL:     "http://127.0.0.1:1",
		HostAddress: host,
		Signer:      peer,
		DirectMux:   true,
		BackoffMin:  10 * time.Millisecond,
		BackoffMax:  50 * time.Millisecond,
	}
	a := acquirePeerConn(cfg)
	b := acquirePeerConn(cfg)
	require.Same(t, a, b)
	require.Equal(t, int32(2), a.refs.Load())
	a.Release()
	require.True(t, PeerConnRegistered(host, "direct"), "first Release must keep the shared PeerConn")
	require.Equal(t, int32(1), b.refs.Load())
	b.Release()
	require.False(t, PeerConnRegistered(host, "direct"))
	select {
	case <-a.done:
	case <-time.After(time.Second):
		t.Fatal("attach loop still running after last Release")
	}
}

func TestRPCClient_CloseIdempotent(t *testing.T) {
	peer := devtest.MustGenerateKey(t)
	host := "gonka1relidem"
	httpClient := NewHTTPClient("http://127.0.0.1:1", "escrow-1", peer)
	rpc := SelectTransport(httpClient, host, ParseRPCEndpoints(EndpointSignatures), nil).(*RPCClient)
	rpc.Close()
	rpc.Close()
	require.False(t, PeerConnRegistered(host, peerConnConfigFromClient(httpClient, host, nil).version()))
}

func TestPeerConn_AcquireReleaseRace(t *testing.T) {
	peer := devtest.MustGenerateKey(t)
	host := "gonka1relrace"
	cfg := PeerConnConfig{
		BaseURL:     "http://127.0.0.1:1",
		HostAddress: host,
		Signer:      peer,
		DirectMux:   true,
		BackoffMin:  5 * time.Millisecond,
		BackoffMax:  20 * time.Millisecond,
	}
	const goroutines = 32
	const iters = 40
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				pc := acquirePeerConn(cfg)
				select {
				case <-pc.done:
					t.Error("acquire returned a closed PeerConn")
				default:
				}
				pc.Release()
			}
		}()
	}
	wg.Wait()
	require.False(t, PeerConnRegistered(host, "direct"))
}
