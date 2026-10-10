package params_test

import (
	"context"
	"testing"

	"common/nodemanager/gen"
	commonruntimeconfig "common/runtimeconfig"
	"devshard/chainoracle/params"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestParseMLNodesEnv(t *testing.T) {
	nodes, err := params.ParseMLNodesEnv("mock-openai-0=http://mock-openai-0:8088,mock-openai-1=http://mock-openai-1:8088")
	require.NoError(t, err)
	require.Len(t, nodes, 2)
	require.Equal(t, "mock-openai-0", nodes[0].ID)
	require.Equal(t, "http://mock-openai-1:8088", nodes[1].Endpoint)
}

func TestParseMLNodesEnv_RejectsBad(t *testing.T) {
	_, err := params.ParseMLNodesEnv("not-an-entry")
	require.Error(t, err)
}

func TestAcquireMLNode_RoundRobinAndExclude(t *testing.T) {
	ctx := context.Background()
	src, err := params.NewCachedSource(ctx, nil, commonruntimeconfig.Snapshot{})
	require.NoError(t, err)

	srv, err := params.NewServer(params.Config{
		Source: src,
		MLNodes: []params.MLNode{
			{ID: "mock-openai-0", Endpoint: "http://mock-openai-0:8088"},
			{ID: "mock-openai-1", Endpoint: "http://mock-openai-1:8088"},
		},
	})
	require.NoError(t, err)

	conn, cleanup := startGRPC(t, srv)
	defer cleanup()
	client := gen.NewNodeManagerClient(conn)

	a, err := client.AcquireMLNode(ctx, &gen.AcquireMLNodeRequest{Model: "m"})
	require.NoError(t, err)
	b, err := client.AcquireMLNode(ctx, &gen.AcquireMLNodeRequest{Model: "m"})
	require.NoError(t, err)
	require.NotEqual(t, a.NodeId, b.NodeId, "pool should round-robin across distinct nodes")

	excluded := a.NodeId
	c, err := client.AcquireMLNode(ctx, &gen.AcquireMLNodeRequest{
		Model:         "m",
		ExcludedNodes: []string{excluded},
	})
	require.NoError(t, err)
	require.NotEqual(t, excluded, c.NodeId)

	_, err = client.AcquireMLNode(ctx, &gen.AcquireMLNodeRequest{
		Model:         "m",
		ExcludedNodes: []string{"mock-openai-0", "mock-openai-1"},
	})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
}

func TestAcquireMLNode_ReleaseAndMaxConcurrent(t *testing.T) {
	ctx := context.Background()
	src, err := params.NewCachedSource(ctx, nil, commonruntimeconfig.Snapshot{})
	require.NoError(t, err)

	srv, err := params.NewServer(params.Config{
		Source: src,
		MLNodes: []params.MLNode{
			{ID: "solo", Endpoint: "http://solo:8088", MaxConcurrent: 1},
		},
	})
	require.NoError(t, err)

	conn, cleanup := startGRPC(t, srv)
	defer cleanup()
	client := gen.NewNodeManagerClient(conn)

	first, err := client.AcquireMLNode(ctx, &gen.AcquireMLNodeRequest{Model: "m"})
	require.NoError(t, err)
	_, err = client.AcquireMLNode(ctx, &gen.AcquireMLNodeRequest{Model: "m"})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))

	_, err = client.ReleaseMLNode(ctx, &gen.ReleaseMLNodeRequest{LockId: first.LockId})
	require.NoError(t, err)

	second, err := client.AcquireMLNode(ctx, &gen.AcquireMLNodeRequest{Model: "m"})
	require.NoError(t, err)
	require.Equal(t, "solo", second.NodeId)
}
