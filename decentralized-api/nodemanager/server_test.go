package nodemanager

import (
	"context"
	"errors"
	"testing"

	"decentralized-api/broker"
	"devshard/nodemanager/gen"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mockBroker implements brokerAcquirer for testing.
type mockBroker struct {
	acquireFunc  func(ctx context.Context, model string, skipNodeIDs []string) (string, string, string, error)
	releaseFunc  func(lockID string, outcome broker.InferenceResult) error
	getNodesFunc func() ([]broker.NodeResponse, error)
}

func (m *mockBroker) AcquireMLNode(ctx context.Context, model string, skipNodeIDs []string) (string, string, string, error) {
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
