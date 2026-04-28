package broker

import (
	"context"
	"testing"
	"time"

	"decentralized-api/apiconfig"

	"github.com/stretchr/testify/require"
)

func TestAcquireMLNode_ReturnsLockAndEndpoint(t *testing.T) {
	b := NewTestBroker()
	node := apiconfig.InferenceNodeConfig{
		Id: "node1", Host: "localhost", InferencePort: 8080,
		InferenceSegment: "/v1", PoCPort: 8081,
		Models:        map[string]apiconfig.ModelConfig{"model1": {}},
		MaxConcurrent: 4,
	}
	registerNodeAndSetInferenceStatus(t, b, node)

	lockID, endpoint, nodeID, err := b.AcquireMLNode(context.Background(), "model1", nil)

	require.NoError(t, err)
	require.NotEmpty(t, lockID)
	require.Equal(t, "http://localhost:8080/v1", endpoint)
	require.Equal(t, "node1", nodeID)
}

func TestAcquireMLNode_NoNodes(t *testing.T) {
	b := NewTestBroker()

	_, _, _, err := b.AcquireMLNode(context.Background(), "model1", nil)

	require.ErrorIs(t, err, ErrNoNodesAvailable)
}

func TestAcquireMLNode_Concurrency(t *testing.T) {
	b := NewTestBroker()
	node := apiconfig.InferenceNodeConfig{
		Id: "node1", Host: "localhost", InferencePort: 8080,
		InferenceSegment: "/v1", PoCPort: 8081,
		Models:        map[string]apiconfig.ModelConfig{"model1": {}},
		MaxConcurrent: 1,
	}
	registerNodeAndSetInferenceStatus(t, b, node)

	lockID, _, _, err := b.AcquireMLNode(context.Background(), "model1", nil)
	require.NoError(t, err)

	_, _, _, err = b.AcquireMLNode(context.Background(), "model1", nil)
	require.ErrorIs(t, err, ErrNoNodesAvailable)

	err = b.ReleaseMLNode(lockID, InferenceSuccess{})
	require.NoError(t, err)

	_, _, _, err = b.AcquireMLNode(context.Background(), "model1", nil)
	require.NoError(t, err)
}

func TestReleaseMLNode_NodeBecomesAvailableAgain(t *testing.T) {
	b := NewTestBroker()
	node := apiconfig.InferenceNodeConfig{
		Id: "node1", Host: "localhost", InferencePort: 8080,
		InferenceSegment: "/v1", PoCPort: 8081,
		Models:        map[string]apiconfig.ModelConfig{"model1": {}},
		MaxConcurrent: 4,
	}
	registerNodeAndSetInferenceStatus(t, b, node)

	lockID, _, _, err := b.AcquireMLNode(context.Background(), "model1", nil)
	require.NoError(t, err)

	err = b.ReleaseMLNode(lockID, InferenceSuccess{})
	require.NoError(t, err)

	// Small wait for the ReleaseNode command to be processed by the broker loop
	time.Sleep(50 * time.Millisecond)

	// Node should be available again
	lockID2, _, _, err := b.AcquireMLNode(context.Background(), "model1", nil)
	require.NoError(t, err)
	require.NotEmpty(t, lockID2)
}

func TestReleaseMLNode_UnknownLockID(t *testing.T) {
	b := NewTestBroker()

	err := b.ReleaseMLNode("does-not-exist", InferenceSuccess{})

	require.ErrorIs(t, err, ErrLockNotFound)
}

func TestEvictExpiredLocks(t *testing.T) {
	b := NewTestBroker()
	node := apiconfig.InferenceNodeConfig{
		Id: "node1", Host: "localhost", InferencePort: 8080,
		InferenceSegment: "/v1", PoCPort: 8081,
		Models:        map[string]apiconfig.ModelConfig{"model1": {}},
		MaxConcurrent: 4,
	}
	registerNodeAndSetInferenceStatus(t, b, node)

	lockID, _, _, err := b.AcquireMLNode(context.Background(), "model1", nil)
	require.NoError(t, err)

	// Backdate the lock to simulate TTL expiry
	b.lockMapMu.Lock()
	entry := b.lockMap[lockID]
	entry.createdAt = time.Now().Add(-25 * time.Minute)
	b.lockMap[lockID] = entry
	b.lockMapMu.Unlock()

	b.evictExpiredLocks()

	// Lock should be gone
	err = b.ReleaseMLNode(lockID, InferenceSuccess{})
	require.ErrorIs(t, err, ErrLockNotFound)

	// The eviction queues a ReleaseNode command; wait for the broker loop to process it.
	time.Sleep(50 * time.Millisecond)
	_, _, _, err = b.AcquireMLNode(context.Background(), "model1", nil)
	require.NoError(t, err)
}
