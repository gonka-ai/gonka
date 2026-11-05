package user

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"common/httpguard"
	"devshard/transport"

	"github.com/stretchr/testify/require"
)

// A host whose on-chain URL resolves to a private address must not be probed
// by WaitRouterCatalog, by IP or by host name.
func TestWaitRouterCatalog_DoesNotDialPrivateHost(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	httpguard.SetAllowPrivate(false)
	t.Cleanup(func() { httpguard.SetAllowPrivate(true) })

	byName := "http://localhost:" + strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	for _, base := range []string{srv.URL, byName} {
		cfg := transport.DefaultClientConfig()
		cfg.RoutePrefix = "/devshard/v2"
		session := &Session{clients: []HostClient{transport.NewHTTPClient(base, "1", nil, cfg)}, escrowID: "1"}

		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		err := session.WaitRouterCatalog(ctx)
		cancel()
		require.ErrorIs(t, err, context.DeadlineExceeded, base)
	}
	require.Equal(t, int32(0), hits.Load())
}
