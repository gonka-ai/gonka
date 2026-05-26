package nodemanager

import (
	"context"
	"errors"
	"testing"

	"decentralized-api/broker"
	"decentralized-api/nodemanager/gen"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mockBroker implements brokerAcquirer for testing.
type mockBroker struct {
	acquireFunc      func(ctx context.Context, model string, skipNodeIDs []string) (string, string, string, error)
	acquireByKeyFunc func(ctx context.Context, recipientKeyID string) (string, string, string, error)
	releaseFunc      func(lockID string, outcome broker.InferenceResult) error
}

func (m *mockBroker) AcquireMLNode(ctx context.Context, model string, skipNodeIDs []string) (string, string, string, error) {
	return m.acquireFunc(ctx, model, skipNodeIDs)
}
func (m *mockBroker) AcquireMLNodeByKey(ctx context.Context, recipientKeyID string) (string, string, string, error) {
	return m.acquireByKeyFunc(ctx, recipientKeyID)
}
func (m *mockBroker) ReleaseMLNode(lockID string, outcome broker.InferenceResult) error {
	return m.releaseFunc(lockID, outcome)
}
func (m *mockBroker) TriggerStatusQuery(_ bool) {}

func TestAcquireMLNode_Success(t *testing.T) {
	srv := NewServer(&mockBroker{
		acquireFunc: func(_ context.Context, _ string, _ []string) (string, string, string, error) {
			return "lock-abc", "http://host:8080/v1", "node-1", nil
		},
	})
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
	})
	_, err := srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{Model: "gpt4"})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
}

func TestAcquireMLNode_QueueFull(t *testing.T) {
	srv := NewServer(&mockBroker{
		acquireFunc: func(_ context.Context, _ string, _ []string) (string, string, string, error) {
			return "", "", "", errors.New("queue full")
		},
	})
	_, err := srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{Model: "gpt4"})
	require.Equal(t, codes.Unavailable, status.Code(err))
}

func TestAcquireMLNode_ByKeyDispatched(t *testing.T) {
	var gotKey string
	var acquireCalled bool
	srv := NewServer(&mockBroker{
		acquireFunc: func(_ context.Context, _ string, _ []string) (string, string, string, error) {
			acquireCalled = true
			return "", "", "", nil
		},
		acquireByKeyFunc: func(_ context.Context, key string) (string, string, string, error) {
			gotKey = key
			return "lock", "http://host:8080/v1", "node-1", nil
		},
	})
	_, err := srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{
		RecipientKeyId: "abcd0123abcd0123",
	})
	require.NoError(t, err)
	require.Equal(t, "abcd0123abcd0123", gotKey)
	require.False(t, acquireCalled, "AcquireMLNode must not be called when recipient_key_id is set")
}

func TestAcquireMLNode_ByKeyUnknownReturnsNotFound(t *testing.T) {
	srv := NewServer(&mockBroker{
		acquireByKeyFunc: func(_ context.Context, _ string) (string, string, string, error) {
			return "", "", "", broker.ErrRecipientKeyUnknown
		},
	})
	_, err := srv.AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{
		RecipientKeyId: "0123456789abcdef",
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestReleaseMLNode_Success(t *testing.T) {
	var gotOutcome broker.InferenceResult
	srv := NewServer(&mockBroker{
		releaseFunc: func(_ string, outcome broker.InferenceResult) error {
			gotOutcome = outcome
			return nil
		},
	})
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
	})
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
	})
	_, err := srv.ReleaseMLNode(context.Background(), &gen.ReleaseMLNodeRequest{LockId: "bad"})
	require.Equal(t, codes.NotFound, status.Code(err))
}
