package transport

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"common/httpguard"
	"connectrpc.com/connect"
	"devshard/observability"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcpb/rpcpbconnect"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// Exercise the clients constructed by NewPeerConn, not just their wrappers.
func TestPeerConnPinnedH2ClientsShareInstrumentation(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	for _, mode := range []string{"grpc", "renewal-probe"} {
		t.Run(mode, func(t *testing.T) {
			ResetRPCH2ClientPoolForTest()
			defer ResetRPCH2ClientPoolForTest()
			entered := make(chan string, 1)
			release := make(chan struct{})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			handler := connect.NewUnaryHandler(rpcpbconnect.PeerAuthServiceAttachProcedure,
				func(ctx context.Context, req *connect.Request[rpcpb.AttachRequest]) (*connect.Response[rpcpb.AttachResponse], error) {
					entered <- req.Header().Get("Content-Type")
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					return connect.NewResponse(&rpcpb.AttachResponse{}), nil
				})
			srv := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
			defer srv.Close()
			defer cancel()
			cfg := PeerConnConfig{BaseURL: srv.URL, HostAddress: t.Name(), DirectMux: true, GRPC: true, MaxConns: 1, DialSet: PeerRPCDialSet{H2URL: srv.URL}}
			pc := NewPeerConn(cfg)
			defer pc.Close()
			client := pc.authDoorGRPC
			if mode == "renewal-probe" {
				client = pc.authHostProbe
			}
			require.NotNil(t, client)
			// An ordinary live request occupies one shared slot already.
			pc.origin.inflight.Store(1)
			before := testutil.ToFloat64(observability.PeerPoolExhaustedCounter(cfg.childID()))
			result := make(chan error, 1)
			go func() { _, err := client.Attach(ctx, connect.NewRequest(&rpcpb.AttachRequest{})); result <- err }()
			select {
			case contentType := <-entered:
				if mode == "grpc" {
					require.Contains(t, contentType, "application/grpc")
				} else {
					require.NotContains(t, contentType, "application/grpc")
				}
			case <-ctx.Done():
				t.Fatal("Attach did not reach H2 handler")
			}
			require.Equal(t, int32(2), pc.origin.inflight.Load())
			require.Equal(t, before+1, testutil.ToFloat64(observability.PeerPoolExhaustedCounter(cfg.childID())))
			close(release)
			require.NoError(t, <-result)
			require.Equal(t, int32(1), pc.origin.inflight.Load(), "completed pinned call releases only its own slot")
			pc.origin.inflight.Store(0)
		})
	}
}
