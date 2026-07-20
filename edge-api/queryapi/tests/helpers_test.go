package queryapitest

import (
	"context"
	"net"
	"net/http/httptest"
	"testing"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/labstack/echo/v4"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"common/chain"
	"edge-api/queryapi"
)

// fakeChain implements queryapi.ChainClient for tests.
type fakeChain struct {
	inferenceQC chain.InferenceClient
	blsQC       blstypes.QueryClient
	restrictQC  restrictionstypes.QueryClient
	cometSC     cmtservice.ServiceClient
}

func (f *fakeChain) InferenceQueryClient() chain.InferenceClient            { return f.inferenceQC }
func (f *fakeChain) BLSQueryClient() blstypes.QueryClient                   { return f.blsQC }
func (f *fakeChain) RestrictionsQueryClient() restrictionstypes.QueryClient { return f.restrictQC }
func (f *fakeChain) CometServiceClient() cmtservice.ServiceClient           { return f.cometSC }

// newHandlers creates Handlers with no gRPC backend — use for handlers that
// don't make any chain calls (e.g. stubs returning 501).
func newHandlers(fc *fakeChain) *queryapi.Handlers {
	return queryapi.NewHandlers(fc)
}

// handlersWithInference starts an in-process gRPC inference server and returns
// Handlers wired to it via bufconn.
func handlersWithInference(t *testing.T, srv inferencetypes.QueryServer) *queryapi.Handlers {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer()
	inferencetypes.RegisterQueryServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("bufconn dial: %v", err)
	}
	t.Cleanup(func() { s.Stop(); _ = lis.Close(); _ = conn.Close() })
	return queryapi.NewHandlers(&fakeChain{inferenceQC: inferencetypes.NewQueryClient(conn)})
}

// handlersWithComet starts an in-process gRPC CometBFT service server and
// returns Handlers wired to it via bufconn.
func handlersWithComet(t *testing.T, srv cmtservice.ServiceServer) *queryapi.Handlers {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer()
	cmtservice.RegisterServiceServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("bufconn dial: %v", err)
	}
	t.Cleanup(func() { s.Stop(); _ = lis.Close(); _ = conn.Close() })
	return queryapi.NewHandlers(&fakeChain{cometSC: cmtservice.NewServiceClient(conn)})
}

// handlersWithInferenceAndComet starts an in-process gRPC server with both inference
// and CometBFT service backends on the same connection.
func handlersWithInferenceAndComet(t *testing.T, inf inferencetypes.QueryServer, comet cmtservice.ServiceServer) *queryapi.Handlers {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer()
	inferencetypes.RegisterQueryServer(s, inf)
	cmtservice.RegisterServiceServer(s, comet)
	go func() { _ = s.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("bufconn dial: %v", err)
	}
	t.Cleanup(func() { s.Stop(); _ = lis.Close(); _ = conn.Close() })
	return queryapi.NewHandlers(&fakeChain{
		inferenceQC: inferencetypes.NewQueryClient(conn),
		cometSC:     cmtservice.NewServiceClient(conn),
	})
}

// handlersWithBLS starts an in-process gRPC BLS query server and returns
// Handlers wired to it via bufconn.
func handlersWithBLS(t *testing.T, srv blstypes.QueryServer) *queryapi.Handlers {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer()
	blstypes.RegisterQueryServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("bufconn dial: %v", err)
	}
	t.Cleanup(func() { s.Stop(); _ = lis.Close(); _ = conn.Close() })
	return queryapi.NewHandlers(&fakeChain{blsQC: blstypes.NewQueryClient(conn)})
}

// handlersWithRestrictions starts an in-process gRPC restrictions server and
// returns Handlers wired to it via bufconn.
func handlersWithRestrictions(t *testing.T, srv restrictionstypes.QueryServer) *queryapi.Handlers {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer()
	restrictionstypes.RegisterQueryServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("bufconn dial: %v", err)
	}
	t.Cleanup(func() { s.Stop(); _ = lis.Close(); _ = conn.Close() })
	return queryapi.NewHandlers(&fakeChain{restrictQC: restrictionstypes.NewQueryClient(conn)})
}

// echoContext creates a minimal echo.Context backed by an httptest recorder.
func echoContext(t *testing.T, method, path string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}
