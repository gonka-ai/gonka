// Package testserver provides a scriptable in-process NodeManager gRPC fake for tests.
package testserver

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devshard/nodemanager/gen"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Handler returns the response for a GetRuntimeConfig call.
type Handler func(ctx context.Context, req *gen.GetRuntimeConfigRequest) (*gen.GetRuntimeConfigResponse, error)

// Server is a scriptable fake NodeManager for runtimeconfig tests.
type Server struct {
	gen.UnimplementedNodeManagerServer

	mu       sync.Mutex
	handlers []Handler
	calls    []*gen.GetRuntimeConfigRequest

	inFlight    int32
	maxInFlight int32
	blockCh     chan struct{}
}

// New returns a Server with no handlers (returns unchanged by default).
func New() *Server {
	return &Server{blockCh: make(chan struct{})}
}

// SetHandlers replaces the handler sequence; each RPC consumes the next handler.
func (s *Server) SetHandlers(handlers ...Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers = append([]Handler(nil), handlers...)
}

// Calls returns a copy of recorded requests.
func (s *Server) Calls() []*gen.GetRuntimeConfigRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*gen.GetRuntimeConfigRequest, len(s.calls))
	copy(out, s.calls)
	return out
}

// MaxInFlight returns the peak concurrent GetRuntimeConfig calls observed.
func (s *Server) MaxInFlight() int32 {
	return atomic.LoadInt32(&s.maxInFlight)
}

// ReleaseBlocked unblocks handlers waiting on BlockNext.
func (s *Server) ReleaseBlocked() {
	select {
	case s.blockCh <- struct{}{}:
	default:
	}
}

// BlockNext makes the next handler wait until ReleaseBlocked or ctx cancel.
func (s *Server) BlockNext() Handler {
	return func(ctx context.Context, req *gen.GetRuntimeConfigRequest) (*gen.GetRuntimeConfigResponse, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.blockCh:
			return unchangedAt(req.GetClientParamsBlockHeight()), nil
		}
	}
}

func (s *Server) GetRuntimeConfig(ctx context.Context, req *gen.GetRuntimeConfigRequest) (*gen.GetRuntimeConfigResponse, error) {
	cur := atomic.AddInt32(&s.inFlight, 1)
	defer atomic.AddInt32(&s.inFlight, -1)
	for {
		max := atomic.LoadInt32(&s.maxInFlight)
		if cur <= max || atomic.CompareAndSwapInt32(&s.maxInFlight, max, cur) {
			break
		}
	}

	reqCopy := &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: req.GetClientParamsBlockHeight(),
		MaxWaitSeconds:          req.GetMaxWaitSeconds(),
	}
	s.mu.Lock()
	s.calls = append(s.calls, reqCopy)
	var h Handler
	if len(s.handlers) > 0 {
		h = s.handlers[0]
		s.handlers = s.handlers[1:]
	}
	s.mu.Unlock()

	if h != nil {
		return h(ctx, req)
	}
	return &gen.GetRuntimeConfigResponse{Unchanged: true}, nil
}

// Dial starts the fake server and returns a connected NodeManagerClient.
func Dial(t *testing.T, srv *Server) gen.NodeManagerClient {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer()
	gen.RegisterNodeManagerServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)
	t.Cleanup(func() { grpcSrv.Stop() })

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return gen.NewNodeManagerClient(conn)
}

func unchangedAt(height int64) *gen.GetRuntimeConfigResponse {
	return &gen.GetRuntimeConfigResponse{Unchanged: true}
}

// FullConfig returns a handler that responds with the given config.
func FullConfig(cfg *gen.RuntimeConfig) Handler {
	return func(_ context.Context, _ *gen.GetRuntimeConfigRequest) (*gen.GetRuntimeConfigResponse, error) {
		return &gen.GetRuntimeConfigResponse{Config: cfg}, nil
	}
}

// Unchanged returns a handler that responds with unchanged=true.
func Unchanged() Handler {
	return func(_ context.Context, _ *gen.GetRuntimeConfigRequest) (*gen.GetRuntimeConfigResponse, error) {
		return &gen.GetRuntimeConfigResponse{Unchanged: true}, nil
	}
}

// DelayedUnchanged sleeps then returns unchanged (simulates 3b server timeout).
func DelayedUnchanged(d time.Duration) Handler {
	return func(ctx context.Context, req *gen.GetRuntimeConfigRequest) (*gen.GetRuntimeConfigResponse, error) {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return unchangedAt(req.GetClientParamsBlockHeight()), nil
		}
	}
}

// Error returns a handler that fails with the given error.
func Error(err error) Handler {
	return func(context.Context, *gen.GetRuntimeConfigRequest) (*gen.GetRuntimeConfigResponse, error) {
		return nil, err
	}
}
