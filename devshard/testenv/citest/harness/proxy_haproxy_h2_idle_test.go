package harness

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

// haproxyKeepAliveIdle outlasts `timeout http-keep-alive 10s` in
// haproxy-rpc.cfg and versiond-router, and stays under `timeout client 60s`.
const haproxyKeepAliveIdle = 14 * time.Second

func newH2CPingClient(t *testing.T, readIdle time.Duration, dials *atomic.Int32) *http.Client {
	t.Helper()
	tr := &http2.Transport{
		AllowHTTP:       true,
		ReadIdleTimeout: readIdle,
		PingTimeout:     2 * time.Second,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			dials.Add(1)
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}
}

// TestProxyHAProxy_H2IdleConnection pins what keeps the peer mux open
// through HAProxy. HTTP/2 PING does not count as activity on a connection
// with no open stream: HAProxy closes it at `timeout http-keep-alive`, and
// the client redials. An open server stream with periodic frames, which is
// what Watch is, holds the one TCP well past that timeout.
func TestProxyHAProxy_H2IdleConnection(t *testing.T) {
	SkipUnlessEnv(t, "TESTENV_CITEST")
	fx := startRPCProxyHAProxyHandler(t, defaultRPCProxyTestLimits(), false, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "PeerAuthService/Watch") {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		f.Flush()
		tick := time.NewTicker(3 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				if _, err := w.Write([]byte("beat\n")); err != nil {
					return
				}
				f.Flush()
			}
		}
	}))
	chat := rpcProxySession + "/devshard.transport.v1.SessionService/Chat"

	t.Run("ping only", func(t *testing.T) {
		var dials atomic.Int32
		client := newH2CPingClient(t, 2*time.Second, &dials)
		require.Equal(t, http.StatusOK, mustProxyRPCStatus(t, client, fx.url, chat))
		require.Equal(t, int32(1), dials.Load())

		time.Sleep(haproxyKeepAliveIdle)
		require.Equal(t, http.StatusOK, mustProxyRPCStatus(t, client, fx.url, chat),
			"the next RPC after HAProxy closes the idle mux must redial and succeed")
		require.Equal(t, int32(2), dials.Load(),
			"PING every 2s must not hold an idle h2 connection past timeout http-keep-alive")
	})

	t.Run("open stream", func(t *testing.T) {
		var dials, beats atomic.Int32
		client := newH2CPingClient(t, 0, &dials)
		require.Equal(t, http.StatusOK, mustProxyRPCStatus(t, client, fx.url, chat))

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			fx.url+"/sessions/_/rpc/devshard.transport.v1.PeerAuthService/Watch", nil)
		require.NoError(t, err)
		resp, err := client.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		t.Cleanup(func() { _ = resp.Body.Close() })
		go func() {
			buf := make([]byte, 64)
			for {
				n, err := resp.Body.Read(buf)
				if n > 0 {
					beats.Add(1)
				}
				if err != nil {
					return
				}
			}
		}()

		time.Sleep(haproxyKeepAliveIdle)
		require.Equal(t, http.StatusOK, mustProxyRPCStatus(t, client, fx.url, chat))
		require.Equal(t, int32(1), dials.Load(), "an open stream with periodic frames must hold the one TCP")
		require.GreaterOrEqual(t, beats.Load(), int32(3), "the held stream kept delivering frames")
	})
}
