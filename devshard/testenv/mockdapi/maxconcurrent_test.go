package mockdapi

import (
	"context"
	"testing"

	"common/nodemanager/gen"
	commrc "common/runtimeconfig"
	"devshard/chainoracle/params"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Test flow:
// 1. Bind ml-a0 to host-a and ml-b0 to host-b, and cap ml-b0 at one lock through MOCK_ML_NODE_MAX_CONCURRENT.
// 2. Acquire from host-b twice: the first gets ml-b0, the second gets ResourceExhausted while host-a still acquires.
// 3. Release the first lock and require host-b to acquire ml-b0 again.
func TestAcquireMLNode_MaxConcurrentPerNode(t *testing.T) {
	nodes := []MLNode{
		{ID: "ml-a0", Endpoint: "http://ml-a0:8088"},
		{ID: "ml-b0", Endpoint: "http://ml-b0:8088"},
	}
	require.NoError(t, ApplyMaxConcurrentEnv(nodes, "ml-b0=1"))
	src, err := params.NewCachedSource(context.Background(), params.StaticFetcher{}, commrc.Snapshot{})
	require.NoError(t, err)
	paramsSrv, err := params.NewServer(params.Config{Source: src, MLNodes: nodes})
	require.NoError(t, err)
	srv := newNodeManagerServer(paramsSrv, nil, nil, false, nodes)
	srv.hosts = newHostPools(nodes,
		map[string]string{"ml-a0": "host-a", "ml-b0": "host-b"},
		map[string]string{"10.0.0.11": "host-a", "10.0.0.12": "host-b"})

	first, err := srv.AcquireMLNode(fromPeer("10.0.0.12"), &gen.AcquireMLNodeRequest{Model: "test-model"})
	require.NoError(t, err)
	require.Equal(t, "ml-b0", first.GetNodeId())
	_, err = srv.AcquireMLNode(fromPeer("10.0.0.12"), &gen.AcquireMLNodeRequest{Model: "test-model"})
	require.Equal(t, codes.ResourceExhausted, status.Code(err), "ml-b0 holds its one lock")
	require.Equal(t, "ml-a0", acquireNode(t, srv, "10.0.0.11"))
	require.Equal(t, "ml-a0", acquireNode(t, srv, "10.0.0.11"), "ml-a0 has no cap")

	_, err = srv.ReleaseMLNode(context.Background(), &gen.ReleaseMLNodeRequest{LockId: first.GetLockId()})
	require.NoError(t, err)
	require.Equal(t, "ml-b0", acquireNode(t, srv, "10.0.0.12"))
	require.Equal(t, uint64(1), srv.hosts.snapshot()["host-b"].AcquireErrors)
}

// Test flow:
// 1. Apply an empty value and require every node to stay unlimited.
// 2. Require a non-positive or non-numeric cap and a node outside the pool to be rejected.
func TestApplyMaxConcurrentEnv(t *testing.T) {
	nodes := []MLNode{{ID: "ml-0"}, {ID: "ml-1"}}
	require.NoError(t, ApplyMaxConcurrentEnv(nodes, ""))
	require.Zero(t, nodes[0].MaxConcurrent)
	require.NoError(t, ApplyMaxConcurrentEnv(nodes, "ml-1=3"))
	require.Equal(t, []int{0, 3}, []int{nodes[0].MaxConcurrent, nodes[1].MaxConcurrent})
	for _, raw := range []string{"ml-0=0", "ml-0=-1", "ml-0=two", "ml-9=1", "ml-0"} {
		require.Error(t, ApplyMaxConcurrentEnv(nodes, raw), raw)
	}
}
