package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

func TestPeerRPCDialSetDefaultOff(t *testing.T) {
	const inference = "http://versiond-router:8080"
	got, err := PeerRPCDialSetFrom(inference, "proxy", "8443", "")
	require.NoError(t, err)
	require.Equal(t, inference, got.InferenceURL)
	require.Empty(t, got.H2URL)
	require.Equal(t, []string{inference}, got.Origins())
}

func TestPeerRPCDialSetUpgradeFalseWithPort(t *testing.T) {
	got, err := PeerRPCDialSetFrom("http://versiond-router:8080", "proxy", "8443", "0")
	require.NoError(t, err)
	require.Empty(t, got.H2URL)
}

func TestPeerRPCDialSetUpgradeSameHost(t *testing.T) {
	got, err := PeerRPCDialSetFrom("http://versiond-router:8080", "", "8443", "1")
	require.NoError(t, err)
	require.Equal(t, "http://versiond-router:8080", got.InferenceURL)
	require.Equal(t, "http://versiond-router:8443", got.H2URL)
	require.Equal(t, []string{
		"http://versiond-router:8080",
		"http://versiond-router:8443",
	}, got.Origins())
}

func TestPeerRPCDialSetOverlayProxyHost(t *testing.T) {
	got, err := PeerRPCDialSetFrom("http://versiond-router:8080", "proxy", "8443", "true")
	require.NoError(t, err)
	require.Equal(t, "http://proxy:8443", got.H2URL)
	require.Equal(t, []string{
		"http://versiond-router:8080",
		"http://proxy:8443",
	}, got.Origins())
}

func TestRPCH2ServerNameIgnoresDialHost(t *testing.T) {
	got, err := PeerRPCDialSetFrom("https://join.example.com:443", "127.0.0.1", "8443", "1")
	require.NoError(t, err)
	require.Equal(t, "https://join.example.com:443", got.InferenceURL)
	require.Equal(t, "https://127.0.0.1:8443", got.H2URL)
	require.Equal(t, "join.example.com", rpcH2ServerName(got.InferenceURL))

	overlay, err := PeerRPCDialSetFrom("https://join.example.com:443/v5", "proxy", "8443", "1")
	require.NoError(t, err)
	require.Equal(t, "https://proxy:8443", overlay.H2URL)
	require.Equal(t, "join.example.com", rpcH2ServerName(overlay.InferenceURL))
	require.NotEqual(t, "proxy", rpcH2ServerName(overlay.InferenceURL))
}

func TestRPCH2ServerName(t *testing.T) {
	require.Equal(t, "join.example.com", rpcH2ServerName("https://join.example.com:443"))
	require.Equal(t, "join.example.com", rpcH2ServerName("https://join.example.com:443/v5"))
	require.Equal(t, "versiond-router", rpcH2ServerName("http://versiond-router:8080"))
	require.Empty(t, rpcH2ServerName(""))
}

func TestPeerRPCDialSetUpgradeWithoutPort(t *testing.T) {
	got, err := PeerRPCDialSetFrom("http://versiond-router:8080", "proxy", "", "1")
	require.NoError(t, err)
	require.Empty(t, got.H2URL, "unset DEVSHARD_RPC_H2_PORT must not prefer h2")
}

func TestPeerRPCDialSetFromEnvDefaultOff(t *testing.T) {
	t.Setenv(envRPCH2Upgrade, "")
	t.Setenv(envRPCH2Host, "proxy")
	t.Setenv(envRPCH2Port, "8443")
	got, err := PeerRPCDialSetFromEnv("http://versiond-router:8080")
	require.NoError(t, err)
	require.Empty(t, got.H2URL)
}

func TestPeerRPCDialSetInvalidPort(t *testing.T) {
	_, err := PeerRPCDialSetFrom("http://versiond-router:8080", "proxy", "nope", "1")
	require.Error(t, err)
}

func TestPeerRPCDialSetEmptyInference(t *testing.T) {
	_, err := PeerRPCDialSetFrom("", "", "", "")
	require.Error(t, err)
}

func TestInferenceHostKey(t *testing.T) {
	require.Equal(t, "versiond-router", inferenceHostKey("http://versiond-router:8080/v5"))
	require.Equal(t, "versiond-router", inferenceHostKey("https://Versiond-Router:443"))
	require.Equal(t, "127.0.0.1", inferenceHostKey("http://127.0.0.1:18080"))
}

func TestH2MissCacheSkipAndTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var cur atomic.Pointer[time.Time]
	cur.Store(&now)
	c := newH2MissCache(DefaultRPCH2MissTTL, func() time.Time { return *cur.Load() })
	c.jitter = func(d time.Duration) time.Duration { return d }
	const host = "versiond-router"
	require.False(t, c.skip(host))
	c.remember(host)
	require.True(t, c.skip(host))
	later := now.Add(DefaultRPCH2MissTTL - time.Second)
	cur.Store(&later)
	require.True(t, c.skip(host))
	expired := now.Add(DefaultRPCH2MissTTL)
	cur.Store(&expired)
	require.False(t, c.skip(host))
}

func TestH2MissCacheTTLJitterDesynchronizesHosts(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var cur atomic.Pointer[time.Time]
	cur.Store(&now)
	ttl := 30 * time.Minute
	var n atomic.Int32
	c := newH2MissCache(ttl, func() time.Time { return *cur.Load() })
	c.jitter = func(d time.Duration) time.Duration {
		if n.Add(1) == 1 {
			return d - d/10
		}
		return d + d/10
	}
	c.remember("host-a")
	c.remember("host-b")
	atLow := now.Add(ttl - ttl/10)
	cur.Store(&atLow)
	require.False(t, c.skip("host-a"), "−10% TTL must expire first")
	require.True(t, c.skip("host-b"))
	atHigh := now.Add(ttl + ttl/10)
	cur.Store(&atHigh)
	require.False(t, c.skip("host-b"), "+10% TTL must still be pinned until then")
}

func TestJitterRPCH2MissTTLStaysInBand(t *testing.T) {
	ttl := DefaultRPCH2MissTTL
	span := time.Duration(float64(ttl) * DefaultRPCH2MissTTLJitter)
	seen := map[time.Duration]struct{}{}
	for i := 0; i < 200; i++ {
		d := jitterRPCH2MissTTL(ttl)
		require.GreaterOrEqual(t, d, ttl-span)
		require.LessOrEqual(t, d, ttl+span)
		seen[d] = struct{}{}
	}
	require.Greater(t, len(seen), 1, "jitter must not be a constant")
}

func TestIsRPCH2Miss(t *testing.T) {
	require.False(t, isRPCH2Miss(nil))
	require.False(t, isRPCH2Miss(errAttachTTL))
	require.False(t, isRPCH2Miss(connect.NewError(connect.CodeUnauthenticated, errors.New("bad token"))))
	require.False(t, isRPCH2Miss(connect.NewError(connect.CodeResourceExhausted, errors.New("rate"))))
	require.False(t, isRPCH2Miss(connect.NewError(connect.CodeUnavailable, errors.New("host initializing"))))
	require.False(t, isRPCH2Miss(connect.NewError(connect.CodeUnavailable, errors.New("connection refused"))))
	require.False(t, isRPCH2Miss(connect.NewError(connect.CodeUnknown, errors.New("decode"))))
	require.True(t, isRPCH2Miss(context.DeadlineExceeded))
	require.True(t, isRPCH2Miss(context.Canceled))
	op := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}
	require.True(t, isRPCH2Miss(op))
	require.True(t, isRPCH2Miss(connect.NewError(connect.CodeUnavailable, op)),
		"connect wraps Do() failures as Unavailable; Unwrap must still see OpError")
	require.True(t, isRPCH2Miss(&net.DNSError{Name: "missing.invalid", IsNotFound: true}))
	require.True(t, isRPCH2Miss(errors.New("http2: client conn could not be established")))
	require.True(t, isRPCH2Miss(connect.NewError(connect.CodeUnavailable, errors.New("http2: failed HTTP/2.0 intro"))))
	require.True(t, isRPCH2Miss(fmt.Errorf("http2: unexpected ALPN protocol %q; want %q", "http/1.1", "h2")))
	require.True(t, isRPCH2Miss(fmt.Errorf("http2: unexpected ALPN protocol %q; want %q", "", "h2")))
	require.True(t, isRPCH2Miss(http2.StreamError{StreamID: 1, Code: http2.ErrCodeProtocol}))
	require.True(t, isRPCH2Miss(http2.ConnectionError(http2.ErrCodeProtocol)))
}
