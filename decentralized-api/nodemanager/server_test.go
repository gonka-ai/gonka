package nodemanager

import (
	"context"
	"errors"
	"strings"
	"testing"

	"common/nodemanager/gen"
	"decentralized-api/broker"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mockBroker implements brokerAcquirer for testing.
type mockBroker struct {
	acquireFunc   func(ctx context.Context, model string, skipNodeIDs []string) (string, string, string, error)
	releaseFunc   func(lockID string, outcome broker.InferenceResult) error
	getNodesFunc  func() ([]broker.NodeResponse, error)
	gotSessionKey string
	gotPrefixes   []uint64
}

func (m *mockBroker) AcquireMLNode(ctx context.Context, model string, skipNodeIDs []string, sessionKey string, prefixHashes []uint64) (string, string, string, error) {
	m.gotSessionKey = sessionKey
	m.gotPrefixes = prefixHashes
	return m.acquireFunc(ctx, model, skipNodeIDs)
}
func (m *mockBroker) ReleaseMLNode(lockID string, outcome broker.InferenceResult) error {
	return m.releaseFunc(lockID, outcome)
}
func (m *mockBroker) TriggerStatusQuery(_ bool) {}
func (m *mockBroker) GetNodes() ([]broker.NodeResponse, error) {
	if m.getNodesFunc == nil {
		return nil, nil
	}
	return m.getNodesFunc()
}

// Test flow:
// 1. Send AcquireMLNode with a session_key and an escrow id to a server over a recording broker.
// 2. Assert the broker received the same session key and the response carries the broker's node.
// 3. Send the same request without a session_key and assert the broker received an empty key.
func TestAcquireMLNode_PassesSessionKeyToBroker(t *testing.T) {
	mb := &mockBroker{
		acquireFunc: func(_ context.Context, _ string, _ []string) (string, string, string, error) {
			return "lock-1", "http://node", "node-1", nil
		},
	}
	srv := NewServer(mb, nil, nil)

	resp, err := srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{Model: "gpt4", EscrowId: "7", SessionKey: "conv-42"})
	require.NoError(t, err)
	require.Equal(t, "conv-42", mb.gotSessionKey)
	require.Equal(t, "node-1", resp.NodeId)

	_, err = srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{Model: "gpt4"})
	require.NoError(t, err)
	require.Equal(t, "", mb.gotSessionKey)
}

// Test flow:
// 1. Send AcquireMLNode with a session_key and prefix_block_hashes to a server over a recording broker.
// 2. Assert the broker received the hashes in order next to the key.
// 3. Send the same request without hashes and assert the broker received none.
func TestAcquireMLNode_PassesPrefixHashesToBroker(t *testing.T) {
	mb := &mockBroker{
		acquireFunc: func(_ context.Context, _ string, _ []string) (string, string, string, error) {
			return "lock-1", "http://node", "node-1", nil
		},
	}
	srv := NewServer(mb, nil, nil)

	_, err := srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{Model: "gpt4", SessionKey: "conv-42", PrefixBlockHashes: []uint64{7, 8, 9}})
	require.NoError(t, err)
	require.Equal(t, "conv-42", mb.gotSessionKey)
	require.Equal(t, []uint64{7, 8, 9}, mb.gotPrefixes)

	_, err = srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{Model: "gpt4", SessionKey: "conv-42"})
	require.NoError(t, err)
	require.Empty(t, mb.gotPrefixes)
}

// Test flow:
// 1. Send AcquireMLNode with a session_key of exactly maxSessionKeyLen bytes and assert the broker received it unchanged.
// 2. Send one with a key one byte longer and assert the broker received an empty key and the acquire still succeeded.
// 3. Send one with a 1 MiB key and assert the same.
func TestAcquireMLNode_DropsSessionKeyLongerThanCap(t *testing.T) {
	mb := &mockBroker{
		acquireFunc: func(_ context.Context, _ string, _ []string) (string, string, string, error) {
			return "lock-1", "http://node", "node-1", nil
		},
	}
	srv := NewServer(mb, nil, nil)

	atCap := strings.Repeat("k", maxSessionKeyLen)
	_, err := srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{Model: "gpt4", SessionKey: atCap})
	require.NoError(t, err)
	require.Equal(t, atCap, mb.gotSessionKey)

	for _, tooLong := range []string{atCap + "k", strings.Repeat("k", 1<<20)} {
		resp, err := srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{Model: "gpt4", SessionKey: tooLong})
		require.NoError(t, err)
		require.Equal(t, "node-1", resp.NodeId)
		require.Equal(t, "", mb.gotSessionKey)
	}
}

func TestAcquireMLNode_Success(t *testing.T) {
	srv := NewServer(&mockBroker{
		acquireFunc: func(_ context.Context, _ string, _ []string) (string, string, string, error) {
			return "lock-abc", "http://host:8080/v1", "node-1", nil
		},
	}, nil, nil)
	resp, err := srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{Model: "gpt4"})
	require.NoError(t, err)
	require.Equal(t, "lock-abc", resp.LockId)
	require.Equal(t, "http://host:8080/v1", resp.Endpoint)
	require.Equal(t, "node-1", resp.NodeId)
}

func TestAcquireMLNode_NoNodes(t *testing.T) {
	srv := NewServer(&mockBroker{
		acquireFunc: func(_ context.Context, _ string, _ []string) (string, string, string, error) {
			return "", "", "", broker.ErrNoNodesAvailable
		},
	}, nil, nil)
	_, err := srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{Model: "gpt4"})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
}

func TestAcquireMLNode_QueueFull(t *testing.T) {
	srv := NewServer(&mockBroker{
		acquireFunc: func(_ context.Context, _ string, _ []string) (string, string, string, error) {
			return "", "", "", errors.New("queue full")
		},
	}, nil, nil)
	_, err := srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{Model: "gpt4"})
	require.Equal(t, codes.Unavailable, status.Code(err))
}

func TestReleaseMLNode_Success(t *testing.T) {
	var gotOutcome broker.InferenceResult
	srv := NewServer(&mockBroker{
		releaseFunc: func(_ string, outcome broker.InferenceResult) error {
			gotOutcome = outcome
			return nil
		},
	}, nil, nil)
	_, err := srv.ReleaseMLNode(context.Background(), &gen.ReleaseMLNodeRequest{
		LockId:  "lock-abc",
		Outcome: gen.ReleaseOutcome_SUCCESS,
	})
	require.NoError(t, err)
	require.IsType(t, broker.InferenceSuccess{}, gotOutcome)
}

func TestReleaseMLNode_TransportError(t *testing.T) {
	var gotOutcome broker.InferenceResult
	srv := NewServer(&mockBroker{
		releaseFunc: func(_ string, outcome broker.InferenceResult) error {
			gotOutcome = outcome
			return nil
		},
	}, nil, nil)
	_, err := srv.ReleaseMLNode(context.Background(), &gen.ReleaseMLNodeRequest{
		LockId:  "lock-abc",
		Outcome: gen.ReleaseOutcome_TRANSPORT_ERROR,
	})
	require.NoError(t, err)
	require.IsType(t, broker.InferenceError{}, gotOutcome)
	require.False(t, gotOutcome.IsSuccess())
}

func TestReleaseMLNode_NotFound(t *testing.T) {
	srv := NewServer(&mockBroker{
		releaseFunc: func(_ string, _ broker.InferenceResult) error {
			return broker.ErrLockNotFound
		},
	}, nil, nil)
	_, err := srv.ReleaseMLNode(context.Background(), &gen.ReleaseMLNodeRequest{LockId: "bad"})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestListNodeCapacity_MapsBrokerNodes(t *testing.T) {
	srv := NewServer(&mockBroker{
		getNodesFunc: func() ([]broker.NodeResponse, error) {
			return []broker.NodeResponse{{
				Node: broker.Node{
					Id:            "node-1",
					MaxConcurrent: 8,
					Models: map[string]broker.ModelArgs{
						"model-a": {},
						"model-b": {},
					},
				},
				State: broker.NodeState{
					CurrentStatus: types.HardwareNodeStatus_INFERENCE,
					LockCount:     3,
				},
			}}, nil
		},
	}, nil, nil)

	resp, err := srv.ListNodeCapacity(context.Background(), &gen.ListNodeCapacityRequest{})
	require.NoError(t, err)
	require.NotZero(t, resp.ServedAtUnix)
	require.Len(t, resp.Nodes, 2)

	byModel := map[string]*gen.NodeCapacityEntry{}
	for _, e := range resp.Nodes {
		byModel[e.Model] = e
		require.Equal(t, "node-1", e.NodeId)
		require.Equal(t, int32(8), e.MaxConcurrent)
		require.Equal(t, int32(3), e.LockCount)
		require.Equal(t, types.HardwareNodeStatus_INFERENCE.String(), e.Status)
	}
	require.Contains(t, byModel, "model-a")
	require.Contains(t, byModel, "model-b")
}

func TestListNodeCapacity_EmptyBroker(t *testing.T) {
	srv := NewServer(&mockBroker{
		getNodesFunc: func() ([]broker.NodeResponse, error) {
			return []broker.NodeResponse{}, nil
		},
	}, nil, nil)
	resp, err := srv.ListNodeCapacity(context.Background(), &gen.ListNodeCapacityRequest{})
	require.NoError(t, err)
	require.Empty(t, resp.Nodes)
	require.NotZero(t, resp.ServedAtUnix)
}

func TestListNodeCapacity_NilBroker(t *testing.T) {
	srv := NewServer(nil, nil, nil)
	_, err := srv.ListNodeCapacity(context.Background(), &gen.ListNodeCapacityRequest{})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}
