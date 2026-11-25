package transport

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/httpguard"
)

// Test flow:
// 1. A host answers only after release is closed and counts its open client connections.
// 2. A PeerConn transport built with the default pool size sends 3x DefaultRPCMaxIdleConnsPerPeer concurrent requests to it, a burst still below DefaultRPCMaxConnsPerPeer.
// 3. Wait until the host has one connection open per request, then release the answers and drain every body.
// 4. Within 5s the host must hold at most DefaultRPCMaxIdleConnsPerPeer connections, not the whole burst parked until IdleConnTimeout.
func TestPeerConnHTTP1BurstKeepsAtMostTheIdleCapOpen(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	const burst = 3 * DefaultRPCMaxIdleConnsPerPeer
	require.Less(t, burst, DefaultRPCMaxConnsPerPeer, "the burst must fit under the connection cap so only the idle cap closes sockets")
	var open atomic.Int64
	release := make(chan struct{})
	host := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write([]byte("ok"))
	}))
	host.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			open.Add(1)
		case http.StateClosed, http.StateHijacked:
			open.Add(-1)
		}
	}
	host.Start()
	t.Cleanup(host.Close)
	var releaseOnce sync.Once
	releaseHandlers := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseHandlers)
	_, roundTripper := newPeerConnTransports(PeerConnConfig{BaseURL: host.URL}, DefaultRPCMaxConnsPerPeer)
	client := &http.Client{Transport: roundTripper}
	t.Cleanup(client.CloseIdleConnections)

	var requests sync.WaitGroup
	for range burst {
		requests.Add(1)
		go func() {
			defer requests.Done()
			response, err := client.Get(host.URL)
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}()
	}
	require.Eventually(t, func() bool { return open.Load() == burst }, 5*time.Second, 10*time.Millisecond,
		"host connections open while %d requests are in flight", burst)
	releaseHandlers()
	requests.Wait()

	require.Eventually(t, func() bool { return open.Load() <= int64(DefaultRPCMaxIdleConnsPerPeer) }, 5*time.Second, 10*time.Millisecond,
		"host connections open after the burst, want at most %d", DefaultRPCMaxIdleConnsPerPeer)
}
