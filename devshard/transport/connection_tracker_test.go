package transport

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHostConnectionTrackerTLSMultiplexedStreams(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		if r.URL.Path == "/hold" {
			<-r.Context().Done()
		}
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	tracker := NewHostConnectionTracker(time.Millisecond)
	var dialer net.Dialer
	tr := &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{RootCAs: server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs},
		DialContext:       tracker.TrackDialContext(dialer.DialContext, "fixture"),
	}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tracker.WrapRoundTripper(tr), Timeout: 5 * time.Second}
	// Establish one TLS/H2 connection before opening simultaneous streams.
	r, err := c.Get(server.URL)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, r.Body)
	require.NoError(t, err)
	require.NoError(t, r.Body.Close())
	r1, err := c.Get(server.URL + "/hold")
	require.NoError(t, err)
	defer r1.Body.Close()
	r2, err := c.Get(server.URL + "/hold")
	require.NoError(t, err)
	defer r2.Body.Close()
	require.Equal(t, 2, r1.ProtoMajor)
	require.Equal(t, int64(1), tracker.Snapshots()[0].Active)
	require.NoError(t, r1.Body.Close())
	require.Equal(t, int64(1), tracker.Snapshots()[0].Active, "another H2 stream still owns the TLS connection")
	require.Zero(t, tracker.Snapshots()[0].Idle)
	require.NoError(t, r2.Body.Close())
	require.Zero(t, tracker.Snapshots()[0].Active)
	require.Equal(t, int64(1), tracker.Snapshots()[0].Idle)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	require.NoError(t, err)
	_, err = c.Do(req)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, tracker.Snapshots()[0].Active)
}

func TestHostConnectionTrackerTracksActiveIdleAndClosedHold(t *testing.T) {
	tracker := NewHostConnectionTracker(10 * time.Millisecond)
	conn := tracker.trackConn(&stubConn{remote: stubAddr("10.0.0.8:443")}, "fallback")

	require.Equal(t, []HostConnectionSnapshot{{
		Address:   "10.0.0.8",
		Active:    1,
		OpenTotal: 1,
	}}, tracker.Snapshots())

	tracker.markIdle(conn)
	require.Equal(t, []HostConnectionSnapshot{{
		Address:   "10.0.0.8",
		Idle:      1,
		OpenTotal: 1,
	}}, tracker.Snapshots())

	tracker.markActive(conn)
	require.Equal(t, []HostConnectionSnapshot{{
		Address:   "10.0.0.8",
		Active:    1,
		OpenTotal: 1,
	}}, tracker.Snapshots())

	require.NoError(t, conn.Close())
	require.Equal(t, []HostConnectionSnapshot{{
		Address:        "10.0.0.8",
		HoldAfterClose: 1,
	}}, tracker.Snapshots())

	require.Eventually(t, func() bool {
		return len(tracker.Snapshots()) == 0
	}, 250*time.Millisecond, 5*time.Millisecond)
}

func TestSnapshotsReturnsEveryTrackedAddressRegardlessOfOrder(t *testing.T) {
	tracker := NewHostConnectionTracker(10 * time.Millisecond)
	tracker.trackConn(&stubConn{remote: stubAddr("10.0.0.9:443")}, "fallback")
	tracker.trackConn(&stubConn{remote: stubAddr("10.0.0.8:443")}, "fallback")

	require.ElementsMatch(t, []HostConnectionSnapshot{
		{Address: "10.0.0.9", Active: 1, OpenTotal: 1},
		{Address: "10.0.0.8", Active: 1, OpenTotal: 1},
	}, tracker.Snapshots())
}

func TestNormalizeConnectionAddressFallsBackToParsedHost(t *testing.T) {
	require.Equal(t, "1.2.3.4", normalizeConnectionAddress(nil, "1.2.3.4:8443"))
	require.Equal(t, "host.internal", normalizeConnectionAddress(nil, "host.internal"))
}

type stubConn struct {
	remote net.Addr
}

func (c *stubConn) Read(_ []byte) (int, error)         { return 0, nil }
func (c *stubConn) Write(b []byte) (int, error)        { return len(b), nil }
func (c *stubConn) Close() error                       { return nil }
func (c *stubConn) LocalAddr() net.Addr                { return stubAddr("127.0.0.1:0") }
func (c *stubConn) RemoteAddr() net.Addr               { return c.remote }
func (c *stubConn) SetDeadline(_ time.Time) error      { return nil }
func (c *stubConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *stubConn) SetWriteDeadline(_ time.Time) error { return nil }

type stubAddr string

func (a stubAddr) Network() string { return "tcp" }
func (a stubAddr) String() string  { return string(a) }
