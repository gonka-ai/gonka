package transport

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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
