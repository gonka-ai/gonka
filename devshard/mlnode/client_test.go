package mlnode

import (
	"context"
	"net"
	"testing"

	"devshard/mlnode/gen"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// mockServer is an in-process gRPC server for testing.
type mockServer struct {
	gen.UnimplementedNodeManagerServer
	acquireFunc func(ctx context.Context, req *gen.AcquireMLNodeRequest) (*gen.AcquireMLNodeResponse, error)
	releaseFunc func(ctx context.Context, req *gen.ReleaseMLNodeRequest) (*gen.ReleaseMLNodeResponse, error)
}

func (m *mockServer) AcquireMLNode(ctx context.Context, req *gen.AcquireMLNodeRequest) (*gen.AcquireMLNodeResponse, error) {
	return m.acquireFunc(ctx, req)
}

func (m *mockServer) ReleaseMLNode(ctx context.Context, req *gen.ReleaseMLNodeRequest) (*gen.ReleaseMLNodeResponse, error) {
	return m.releaseFunc(ctx, req)
}

func startMockServer(t *testing.T, srv *mockServer) *Client {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	grpcSrv := grpc.NewServer()
	gen.RegisterNodeManagerServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)
	t.Cleanup(grpcSrv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return &Client{conn: conn, client: gen.NewNodeManagerClient(conn)}
}

func TestClient_Acquire_Success(t *testing.T) {
	srv := &mockServer{
		acquireFunc: func(_ context.Context, req *gen.AcquireMLNodeRequest) (*gen.AcquireMLNodeResponse, error) {
			assert.Equal(t, "model-a", req.Model)
			assert.Equal(t, []string{"bad-node"}, req.ExcludedNodes)
			return &gen.AcquireMLNodeResponse{LockId: "lock-1", Endpoint: "http://node1:8080", NodeId: "node-1"}, nil
		},
	}

	c := startMockServer(t, srv)
	resp, err := c.Acquire(context.Background(), "model-a", []string{"bad-node"})

	require.NoError(t, err)
	assert.Equal(t, "http://node1:8080", resp.Endpoint)
	assert.Equal(t, "lock-1", resp.LockId)
	assert.Equal(t, "node-1", resp.NodeId)
}

func TestClient_Acquire_NoNodesAvailable(t *testing.T) {
	srv := &mockServer{
		acquireFunc: func(_ context.Context, _ *gen.AcquireMLNodeRequest) (*gen.AcquireMLNodeResponse, error) {
			return nil, status.Error(codes.ResourceExhausted, "no nodes available")
		},
	}

	c := startMockServer(t, srv)
	_, err := c.Acquire(context.Background(), "model-a", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no nodes available")
}

func TestClient_Acquire_ServerError(t *testing.T) {
	srv := &mockServer{
		acquireFunc: func(_ context.Context, _ *gen.AcquireMLNodeRequest) (*gen.AcquireMLNodeResponse, error) {
			return nil, status.Error(codes.Unavailable, "queue full")
		},
	}

	c := startMockServer(t, srv)
	_, err := c.Acquire(context.Background(), "model-a", nil)

	require.Error(t, err)
}

func TestClient_Release_AllOutcomes(t *testing.T) {
	outcomes := []gen.ReleaseOutcome{
		gen.ReleaseOutcome_SUCCESS,
		gen.ReleaseOutcome_TRANSPORT_ERROR,
		gen.ReleaseOutcome_APPLICATION_ERROR,
		gen.ReleaseOutcome_TIMEOUT,
	}

	for _, outcome := range outcomes {
		t.Run(outcome.String(), func(t *testing.T) {
			var gotOutcome gen.ReleaseOutcome
			srv := &mockServer{
				releaseFunc: func(_ context.Context, req *gen.ReleaseMLNodeRequest) (*gen.ReleaseMLNodeResponse, error) {
					assert.Equal(t, "lock-1", req.LockId)
					gotOutcome = req.Outcome
					return &gen.ReleaseMLNodeResponse{}, nil
				},
			}
			c := startMockServer(t, srv)
			err := c.Release(context.Background(), "lock-1", outcome)
			require.NoError(t, err)
			assert.Equal(t, outcome, gotOutcome)
		})
	}
}

func TestNew_And_Close(t *testing.T) {
	c, err := NewClient("localhost:19999")
	require.NoError(t, err)
	assert.NoError(t, c.Close())
}

func TestClient_Release_Error(t *testing.T) {
	srv := &mockServer{
		releaseFunc: func(_ context.Context, _ *gen.ReleaseMLNodeRequest) (*gen.ReleaseMLNodeResponse, error) {
			return nil, status.Error(codes.NotFound, "lock not found")
		},
	}

	c := startMockServer(t, srv)
	err := c.Release(context.Background(), "lock-1", gen.ReleaseOutcome_SUCCESS)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "lock-1")
}
