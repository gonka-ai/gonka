package transport_test

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"common/httpguard"
	devshardpkg "devshard"
	devtest "devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport"
	"devshard/transport/rpcpb/rpcpbconnect"
	"devshard/transport/rpcserver"
)

type acceptCountListener struct {
	net.Listener
	n *atomic.Int32
}

func (l *acceptCountListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.n.Add(1)
	return c, nil
}

type countedH2Host struct {
	srv     *httptest.Server
	accepts *atomic.Int32
	watches *atomic.Int32
	proto   atomic.Value
}

func startCountedH2Host(t *testing.T, hostAddr string) *countedH2Host {
	t.Helper()
	auth := rpcserver.NewPeerAuthHandler(signing.NewSecp256k1Verifier(), hostAddr, rpcserver.PeerAuthConfig{
		Heartbeat: 50 * time.Millisecond,
	})
	mux := rpcserver.NewMux(auth, rpcserver.NewSessionHandler(nil))
	host := &countedH2Host{accepts: new(atomic.Int32), watches: new(atomic.Int32)}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host.proto.Store(r.Proto)
		escrow, procedure, ok := cutSessionRPC(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(procedure, rpcpbconnect.PeerAuthServiceWatchProcedure) {
			host.watches.Add(1)
			defer host.watches.Add(-1)
		}
		u := *r.URL
		u.Path = procedure
		r2 := r.WithContext(rpcserver.WithEscrowID(r.Context(), escrow))
		r2.URL = &u
		mux.ServeHTTP(w, r2)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(h2c.NewHandler(h, &http2.Server{}))
	srv.Listener = &acceptCountListener{Listener: ln, n: host.accepts}
	srv.Start()
	t.Cleanup(srv.Close)
	t.Cleanup(auth.Close)
	host.srv = srv
	return host
}

// cutSessionRPC splits /devshard/{ver}/sessions/{escrow}/rpc/{procedure}.
func cutSessionRPC(path string) (escrow, procedure string, ok bool) {
	const marker = "/sessions/"
	const rpc = "/rpc"
	i := strings.LastIndex(path, marker)
	if i < 0 {
		return "", "", false
	}
	rest := path[i+len(marker):]
	j := strings.Index(rest, rpc)
	if j < 0 {
		return "", "", false
	}
	escrow = rest[:j]
	procedure = rest[j+len(rpc):]
	if escrow == "" {
		return "", "", false
	}
	if procedure == "" {
		procedure = "/"
	}
	if !strings.HasPrefix(procedure, "/") {
		procedure = "/" + procedure
	}
	return escrow, procedure, true
}

func newShardPeerConn(t *testing.T, hostAddr, escrowID, baseURL, h2URL string, signer signing.Signer) *transport.PeerConn {
	t.Helper()
	pc := transport.NewPeerConn(transport.PeerConnConfig{
		BaseURL:      baseURL,
		RoutePrefix:  devshardpkg.DefaultRoutePrefix(),
		DoorEscrowID: escrowID,
		HostAddress:  hostAddr,
		Signer:       signer,
		DialSet: transport.PeerRPCDialSet{
			InferenceURL: baseURL,
			H2URL:        h2URL,
		},
		H2ProbeTimeout: 200 * time.Millisecond,
		WatchStale:     time.Minute,
	})
	t.Cleanup(pc.Close)
	return pc
}

func waitPeerReadyTimeout(t *testing.T, pc *transport.PeerConn, d time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		return pc.Ready() && len(pc.LiveToken()) > 0
	}, d, 10*time.Millisecond)
}

// TestPeerConn_TenEscrowsShareOneH2Conn is one process in 10 escrows talking to
// one host. Sessions stay per (host, version, BaseURL, signer); the TCP+h2
// mux to that host is shared.
func TestPeerConn_TenEscrowsShareOneH2Conn(t *testing.T) {
	const n = 10
	httpguard.SetAllowPrivate(true)
	transport.ResetRPCH2MissCacheForTest()
	t.Cleanup(transport.ResetRPCH2MissCacheForTest)

	hostAddr := devtest.MustGenerateKey(t).Address()
	host := startCountedH2Host(t, hostAddr)

	pcs := make([]*transport.PeerConn, n)
	for i := 0; i < n; i++ {
		escrow := fmt.Sprintf("escrow-%02d", i)
		base := fmt.Sprintf("http://shard-%02d.invalid:8080", i)
		pcs[i] = newShardPeerConn(t, hostAddr, escrow, base, host.srv.URL, devtest.MustGenerateKey(t))
		pcs[i].Start()
	}
	for i, pc := range pcs {
		waitPeerReadyTimeout(t, pc, 5*time.Second)
		require.True(t, pc.UsingH2(), "escrow %d must stay on h2", i)
	}

	tokens := make(map[string]struct{}, n)
	for i, pc := range pcs {
		tok := string(pc.LiveToken())
		_, dup := tokens[tok]
		require.False(t, dup, "escrow %d reused another session token", i)
		tokens[tok] = struct{}{}
	}
	require.Len(t, tokens, n)

	require.Equal(t, int32(1), host.accepts.Load(), "10 escrows to one host must share one TCP")
	require.Equal(t, int32(n), host.watches.Load(), "each escrow keeps its own Watch stream")
	require.Equal(t, "HTTP/2.0", host.proto.Load())

	for _, i := range []int{0, 3, 7} {
		pcs[i].Close()
	}
	require.Eventually(t, func() bool {
		return host.watches.Load() == n-3
	}, 2*time.Second, 10*time.Millisecond, "closed sessions must drop their Watch")

	for i, pc := range pcs {
		if i == 0 || i == 3 || i == 7 {
			continue
		}
		require.True(t, pc.Ready(), "escrow %d must survive a sibling Close", i)
		require.True(t, pc.UsingH2(), "escrow %d must keep the shared mux", i)
	}
	require.Equal(t, int32(1), host.accepts.Load(), "closing some sessions must not dial a second TCP")
}

func TestPeerConn_DistinctH2HostsDoNotShareConn(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	transport.ResetRPCH2MissCacheForTest()
	t.Cleanup(transport.ResetRPCH2MissCacheForTest)

	hostA := devtest.MustGenerateKey(t).Address()
	hostB := devtest.MustGenerateKey(t).Address()
	a := startCountedH2Host(t, hostA)
	b := startCountedH2Host(t, hostB)

	pcA := newShardPeerConn(t, hostA, "escrow-a", "http://shard-a.invalid:8080", a.srv.URL, devtest.MustGenerateKey(t))
	pcB := newShardPeerConn(t, hostB, "escrow-b", "http://shard-b.invalid:8080", b.srv.URL, devtest.MustGenerateKey(t))
	pcA.Start()
	pcB.Start()
	waitPeerReadyTimeout(t, pcA, 5*time.Second)
	waitPeerReadyTimeout(t, pcB, 5*time.Second)
	require.True(t, pcA.UsingH2())
	require.True(t, pcB.UsingH2())
	require.Equal(t, int32(1), a.accepts.Load())
	require.Equal(t, int32(1), b.accepts.Load())
}
