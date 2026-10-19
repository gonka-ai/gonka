package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

func TestH2DiscoveryErrorClasses(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want h2MissKind
	}{
		{"EOF", io.EOF, h2Closed},
		{"short read", io.ErrUnexpectedEOF, h2Closed},
		{"reset", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, h2Closed},
		{"broken pipe", syscall.EPIPE, h2Closed},
		{"connection error", http2.ConnectionError(http2.ErrCodeProtocol), h2Closed},
		{"stream error", http2.StreamError{StreamID: 1, Code: http2.ErrCodeRefusedStream}, h2NotMiss},
		{"cancel", context.Canceled, h2NotMiss},
		{"probe deadline", rpch2ProbeTimeoutError(context.DeadlineExceeded), h2NotMiss},
		{"dial timeout", &net.DNSError{IsTimeout: true}, h2NotMiss},
		{"DNS", &net.DNSError{IsNotFound: true}, h2NotMiss},
		{"refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, h2NotMiss},
		{"local h2 disabled", errors.New("http2: unencrypted HTTP/2 not enabled"), h2NotMiss},
		{"ALPN", errors.New("http2: unexpected ALPN protocol"), h2ProtocolMiss},
		{"TLS alert", errors.New("remote error: tls: no application protocol"), h2ProtocolMiss},
		{"nginx H1", errors.New("http2: failed reading the frame payload: http2: frame too large, note that the frame header looked like an HTTP/1.1 header"), h2ProtocolMiss},
		{"wire refusal", connect.NewWireError(connect.CodeUnavailable, errors.New("http2: unexpected ALPN protocol")), h2NotMiss},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, classifyRPCH2Miss(tc.err))
			require.Equal(t, tc.want, classifyRPCH2Miss(fmt.Errorf("attach: %w", tc.err)))
		})
	}
}

func TestH2BackoffRetainsHistoryAndSuccessClearsIt(t *testing.T) {
	now := time.Unix(1700000000, 0)
	c := newH2MissCache(30*time.Minute, func() time.Time { return now })
	c.jitter = nil
	for _, want := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
		generation, ok := c.begin("origin")
		require.True(t, ok)
		c.finish("origin", generation, io.EOF)
		require.Equal(t, want, c.entries["origin"].until.Sub(now))
		require.True(t, c.skip("origin"))
		now = now.Add(want)
		require.False(t, c.skip("origin"))
	}
	generation, ok := c.begin("origin")
	require.True(t, ok)
	c.finish("origin", generation, nil)
	require.Empty(t, c.entries)
	// A stale failure cannot undo success or consume the next probe's lease.
	next, ok := c.begin("origin")
	require.True(t, ok)
	c.finish("origin", generation, io.EOF)
	require.True(t, c.entries["origin"].probing)
	c.finish("origin", next, io.EOF)
	require.Equal(t, 30*time.Second, c.entries["origin"].until.Sub(now))
}

func TestH2DiscoveryOneProbePerOrigin(t *testing.T) {
	c := newH2MissCache(time.Minute, nil)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := c.begin("origin"); ok {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), winners.Load())
	_, ok := c.begin("other origin")
	require.True(t, ok)
}

func TestH2OriginCanonicalization(t *testing.T) {
	for _, pair := range [][2]string{
		{"http://HOST/a", "http://host:80/b?q=1"},
		{"https://HOST", "https://host:443"},
		{"http://[2001:db8::1]", "http://[2001:db8:0:0::1]:80/v6"},
	} {
		require.Equal(t, inferenceHostKey(pair[0]), inferenceHostKey(pair[1]))
	}
	require.NotEqual(t, inferenceHostKey("http://host"), inferenceHostKey("https://host"))
	require.NotEqual(t, inferenceHostKey("http://host:80"), inferenceHostKey("http://host:8080"))
}
