package broker

import (
	"decentralized-api/mlnodeclient"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
)

// Mock ML client for testing
type mockMLClient struct{}

func (m *mockMLClient) GetPowStatus() (*mlnodeclient.PowStatusResponse, error) {
	return &mlnodeclient.PowStatusResponse{Status: mlnodeclient.POW_IDLE}, nil
}

func (m *mockMLClient) NodeState() (*mlnodeclient.StateResponse, error) {
	return &mlnodeclient.StateResponse{State: mlnodeclient.MlNodeState_STOPPED}, nil
}

func (m *mockMLClient) Stop() error {
	return nil
}

func (m *mockMLClient) InitGenerate(dto mlnodeclient.InitDto) error {
	return nil
}

func (m *mockMLClient) InferenceHealth() (bool, error) {
	return true, nil
}

func (m *mockMLClient) InferenceUp(model string, args []string) error {
	return nil
}

func (m *mockMLClient) StartTraining(taskId uint64, participant string, nodeId string, masterNodeAddr string, rank int, worldSize int) error {
	return nil
}

func createTestNode(id string) *NodeWithState {
	return &NodeWithState{
		Node: Node{
			Id:               id,
			Host:             "test-host",
			InferencePort:    8080,
			PoCPort:          8081,
			InferenceSegment: "/inference",
			PoCSegment:       "/poc",
			MaxConcurrent:    5,
			NodeNum:          1,
		},
		State: NodeState{
			Status:         types.HardwareNodeStatus_UNKNOWN,
			IntendedStatus: types.HardwareNodeStatus_UNKNOWN,
		},
	}
}

func TestNodeWorker_BasicOperation(t *testing.T) {
	node := createTestNode("test-node-1")
	worker := NewNodeWorker("test-node-1", node)
	defer worker.Shutdown()

	// Test successful job submission
	executed := false
	success := worker.Submit(func() error {
		executed = true
		return nil
	})

	assert.True(t, success, "Job submission should succeed")

	// Wait for job execution
	time.Sleep(50 * time.Millisecond)
	assert.True(t, executed, "Job should have been executed")
}

func TestNodeWorker_ErrorHandling(t *testing.T) {
	node := createTestNode("test-node-1")
	worker := NewNodeWorker("test-node-1", node)
	defer worker.Shutdown()

	// Submit job that returns error
	testErr := errors.New("test error")
	success := worker.Submit(func() error {
		return testErr
	})

	assert.True(t, success, "Job submission should succeed even if job will error")

	// Wait for job execution
	time.Sleep(50 * time.Millisecond)
	// No panic should occur, error should be logged
}

func TestNodeWorker_QueueFull(t *testing.T) {
	node := createTestNode("test-node-1")
	worker := NewNodeWorker("test-node-1", node)
	defer worker.Shutdown()

	// Fill the queue with slow jobs
	slowJobSubmitted := 0
	for i := 0; i < 10; i++ { // Queue size is 10
		success := worker.Submit(func() error {
			time.Sleep(100 * time.Millisecond)
			return nil
		})
		if success {
			slowJobSubmitted++
		}
	}

	assert.Equal(t, 10, slowJobSubmitted, "Should submit exactly 10 jobs (queue size)")

	// Try to submit one more - should fail
	success := worker.Submit(func() error {
		return nil
	})

	assert.False(t, success, "Job submission should fail when queue is full")
}

func TestNodeWorker_GracefulShutdown(t *testing.T) {
	node := createTestNode("test-node-1")
	worker := NewNodeWorker("test-node-1", node)

	// Submit jobs that will execute during shutdown
	var executedCount int32
	for i := 0; i < 5; i++ {
		worker.Submit(func() error {
			atomic.AddInt32(&executedCount, 1)
			time.Sleep(10 * time.Millisecond)
			return nil
		})
	}

	// Give first job time to start
	time.Sleep(5 * time.Millisecond)

	// Shutdown should wait for all jobs
	worker.Shutdown()

	assert.Equal(t, int32(5), atomic.LoadInt32(&executedCount),
		"All queued jobs should execute before shutdown completes")
}

func TestNodeWorkGroup_AddRemoveWorkers(t *testing.T) {
	group := NewNodeWorkGroup()

	// Add workers
	node1 := createTestNode("node-1")
	node2 := createTestNode("node-2")

	worker1 := NewNodeWorker("node-1", node1)
	worker2 := NewNodeWorker("node-2", node2)

	group.AddWorker("node-1", worker1)
	group.AddWorker("node-2", worker2)

	// Check workers exist
	w1, exists1 := group.GetWorker("node-1")
	w2, exists2 := group.GetWorker("node-2")

	assert.True(t, exists1, "Worker 1 should exist")
	assert.True(t, exists2, "Worker 2 should exist")
	assert.Equal(t, worker1, w1)
	assert.Equal(t, worker2, w2)

	// Remove worker
	group.RemoveWorker("node-1")

	_, exists1 = group.GetWorker("node-1")
	assert.False(t, exists1, "Worker 1 should not exist after removal")
}

func TestNodeWorkGroup_ExecuteOnAll(t *testing.T) {
	group := NewNodeWorkGroup()

	// Add 3 workers
	nodes := make([]*NodeWithState, 3)
	for i := 0; i < 3; i++ {
		nodeId := fmt.Sprintf("node-%d", i)
		nodes[i] = createTestNode(nodeId)
		worker := NewNodeWorker(nodeId, nodes[i])
		group.AddWorker(nodeId, worker)
	}

	// Execute job on all nodes
	var executedCount int32
	var executedNodes sync.Map

	submitted, failed := group.ExecuteOnAll(func(nodeId string, node *NodeWithState) func() error {
		return func() error {
			atomic.AddInt32(&executedCount, 1)
			executedNodes.Store(nodeId, true)
			return nil
		}
	})

	assert.Equal(t, 3, submitted, "Should submit to all 3 nodes")
	assert.Equal(t, 0, failed, "No submissions should fail")
	assert.Equal(t, int32(3), atomic.LoadInt32(&executedCount), "All jobs should execute")

	// Verify all nodes executed
	for i := 0; i < 3; i++ {
		nodeId := fmt.Sprintf("node-%d", i)
		_, executed := executedNodes.Load(nodeId)
		assert.True(t, executed, "Node %s should have executed", nodeId)
	}
}

func TestNodeWorkGroup_ExecuteOnNodes(t *testing.T) {
	group := NewNodeWorkGroup()

	// Add 5 workers
	for i := 0; i < 5; i++ {
		nodeId := fmt.Sprintf("node-%d", i)
		node := createTestNode(nodeId)
		worker := NewNodeWorker(nodeId, node)
		group.AddWorker(nodeId, worker)
	}

	// Execute on specific nodes only
	targetNodes := []string{"node-1", "node-3"}
	var executedNodes sync.Map

	submitted, failed := group.ExecuteOnNodes(targetNodes, func(nodeId string, node *NodeWithState) func() error {
		return func() error {
			executedNodes.Store(nodeId, true)
			return nil
		}
	})

	assert.Equal(t, 2, submitted, "Should submit to 2 specific nodes")
	assert.Equal(t, 0, failed, "No submissions should fail")

	// Verify only target nodes executed
	for i := 0; i < 5; i++ {
		nodeId := fmt.Sprintf("node-%d", i)
		_, executed := executedNodes.Load(nodeId)

		if nodeId == "node-1" || nodeId == "node-3" {
			assert.True(t, executed, "Target node %s should have executed", nodeId)
		} else {
			assert.False(t, executed, "Non-target node %s should not have executed", nodeId)
		}
	}
}

func TestNodeWorkGroup_ConcurrentExecution(t *testing.T) {
	group := NewNodeWorkGroup()

	// Add multiple workers
	nodeCount := 10
	for i := 0; i < nodeCount; i++ {
		nodeId := fmt.Sprintf("node-%d", i)
		node := createTestNode(nodeId)
		worker := NewNodeWorker(nodeId, node)
		group.AddWorker(nodeId, worker)
	}

	// Track execution timing
	startTimes := sync.Map{}
	endTimes := sync.Map{}

	startTime := time.Now()

	submitted, failed := group.ExecuteOnAll(func(nodeId string, node *NodeWithState) func() error {
		return func() error {
			startTimes.Store(nodeId, time.Now())
			time.Sleep(50 * time.Millisecond) // Simulate work
			endTimes.Store(nodeId, time.Now())
			return nil
		}
	})

	totalDuration := time.Since(startTime)

	assert.Equal(t, nodeCount, submitted)
	assert.Equal(t, 0, failed)

	// If executed sequentially, would take nodeCount * 50ms
	// With parallel execution, should be close to 50ms
	assert.Less(t, totalDuration, time.Duration(nodeCount*30)*time.Millisecond,
		"Execution should be parallel, not sequential")
}

func TestNodeWorkGroup_ThreadSafety(t *testing.T) {
	group := NewNodeWorkGroup()

	// Concurrently add/remove workers and execute jobs
	var wg sync.WaitGroup

	// Worker adder goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			nodeId := fmt.Sprintf("node-%d", i)
			node := createTestNode(nodeId)
			worker := NewNodeWorker(nodeId, node)
			group.AddWorker(nodeId, worker)
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Worker remover goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(30 * time.Millisecond) // Let some workers be added first
		for i := 0; i < 10; i++ {
			nodeId := fmt.Sprintf("node-%d", i)
			group.RemoveWorker(nodeId)
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Job executor goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			time.Sleep(20 * time.Millisecond)
			group.ExecuteOnAll(func(nodeId string, node *NodeWithState) func() error {
				return func() error {
					// Simple job
					return nil
				}
			})
		}
	}()

	// Wait for all operations to complete
	wg.Wait()

	// No panics or race conditions should occur
	// Final state check
	remainingWorkers := 0
	for i := 0; i < 20; i++ {
		nodeId := fmt.Sprintf("node-%d", i)
		if _, exists := group.GetWorker(nodeId); exists {
			remainingWorkers++
		}
	}

	assert.Equal(t, 10, remainingWorkers, "Should have 10 workers remaining (20 added - 10 removed)")
}

func TestNodeWorkGroup_ErrorPropagation(t *testing.T) {
	group := NewNodeWorkGroup()

	// Add workers
	for i := 0; i < 3; i++ {
		nodeId := fmt.Sprintf("node-%d", i)
		node := createTestNode(nodeId)
		worker := NewNodeWorker(nodeId, node)
		group.AddWorker(nodeId, worker)
	}

	// Execute jobs where some fail
	var successCount int32
	var errorCount int32

	submitted, failed := group.ExecuteOnAll(func(nodeId string, node *NodeWithState) func() error {
		return func() error {
			if nodeId == "node-1" {
				atomic.AddInt32(&errorCount, 1)
				return errors.New("simulated error")
			}
			atomic.AddInt32(&successCount, 1)
			return nil
		}
	})

	assert.Equal(t, 3, submitted)
	assert.Equal(t, 0, failed) // Failed means couldn't submit, not job error
	assert.Equal(t, int32(2), atomic.LoadInt32(&successCount), "2 jobs should succeed")
	assert.Equal(t, int32(1), atomic.LoadInt32(&errorCount), "1 job should error")
}
