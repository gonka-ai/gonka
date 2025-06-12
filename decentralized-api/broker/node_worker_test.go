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
			CurrentStatus:  types.HardwareNodeStatus_UNKNOWN,
			IntendedStatus: types.HardwareNodeStatus_UNKNOWN,
			AdminState: AdminState{
				Enabled: true,
				Epoch:   0,
			},
		},
	}
}

func TestNodeWorker_BasicOperation(t *testing.T) {
	node := createTestNode("test-node-1")
	mockClient := mlnodeclient.NewMockClient()
	worker := NewNodeWorkerWithClient("test-node-1", node, mockClient)
	defer worker.Shutdown()

	// Test successful command submission
	executed := false
	cmd := &TestCommand{
		ExecuteFn: func(worker *NodeWorker) error {
			executed = true
			return nil
		},
	}
	success := worker.Submit(cmd)

	assert.True(t, success, "Command submission should succeed")

	// Wait for command execution
	time.Sleep(50 * time.Millisecond)
	assert.True(t, executed, "Command should have been executed")
}

func TestNodeWorker_ErrorHandling(t *testing.T) {
	node := createTestNode("test-node-1")
	mockClient := mlnodeclient.NewMockClient()
	worker := NewNodeWorkerWithClient("test-node-1", node, mockClient)
	defer worker.Shutdown()

	// Submit command that returns error
	testErr := errors.New("test error")
	cmd := &TestCommand{
		ExecuteFn: func(worker *NodeWorker) error {
			return testErr
		},
	}
	success := worker.Submit(cmd)

	assert.True(t, success, "Command submission should succeed even if command will error")

	// Wait for command execution
	time.Sleep(50 * time.Millisecond)
	// No panic should occur, error should be logged
}

func TestNodeWorker_QueueFull(t *testing.T) {
	node := createTestNode("test-node-1")
	mockClient := mlnodeclient.NewMockClient()
	worker := NewNodeWorkerWithClient("test-node-1", node, mockClient)
	defer worker.Shutdown()

	// Fill the queue with slow commands
	slowCmdSubmitted := 0
	for i := 0; i < 10; i++ { // Queue size is 10
		cmd := &TestCommand{
			ExecuteFn: func(worker *NodeWorker) error {
				time.Sleep(100 * time.Millisecond)
				return nil
			},
		}
		success := worker.Submit(cmd)
		if success {
			slowCmdSubmitted++
		}
	}

	assert.Equal(t, 10, slowCmdSubmitted, "Should submit exactly 10 commands (queue size)")

	// Try to submit one more - should fail
	cmd := &TestCommand{ExecuteFn: func(worker *NodeWorker) error { return nil }}
	success := worker.Submit(cmd)

	assert.False(t, success, "Command submission should fail when queue is full")
}

func TestNodeWorker_GracefulShutdown(t *testing.T) {
	node := createTestNode("test-node-1")
	mockClient := mlnodeclient.NewMockClient()
	worker := NewNodeWorkerWithClient("test-node-1", node, mockClient)

	// Submit commands that will execute during shutdown
	var executedCount int32
	for i := 0; i < 5; i++ {
		cmd := &TestCommand{
			ExecuteFn: func(worker *NodeWorker) error {
				atomic.AddInt32(&executedCount, 1)
				time.Sleep(10 * time.Millisecond)
				return nil
			},
		}
		worker.Submit(cmd)
	}

	// Give first command time to start
	time.Sleep(5 * time.Millisecond)

	// Shutdown should wait for all commands
	worker.Shutdown()

	assert.Equal(t, int32(5), atomic.LoadInt32(&executedCount),
		"All queued commands should execute before shutdown completes")
}

func TestNodeWorker_MLClientInteraction(t *testing.T) {
	node := createTestNode("test-node-1")
	mockClient := mlnodeclient.NewMockClient()
	worker := NewNodeWorkerWithClient("test-node-1", node, mockClient)
	defer worker.Shutdown()

	// Test Stop operation
	stopCmd := StopNodeCommand{}
	worker.Submit(stopCmd)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, mockClient.StopCalled, "Stop should be called once")
	assert.Equal(t, mlnodeclient.MlNodeState_STOPPED, mockClient.CurrentState, "State should be STOPPED")

	// Test InferenceUp operation
	node.Node.Models = map[string]ModelArgs{
		"test-model": {Args: []string{"--arg1", "--arg2"}},
	}
	inferenceCmd := InferenceUpNodeCommand{}
	worker.Submit(inferenceCmd)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, mockClient.InferenceUpCalled, "InferenceUp should be called once")
	assert.Equal(t, "test-model", mockClient.LastInferenceModel, "Model should be captured")
	assert.Equal(t, []string{"--arg1", "--arg2"}, mockClient.LastInferenceArgs, "Args should be captured")
	assert.Equal(t, mlnodeclient.MlNodeState_INFERENCE, mockClient.CurrentState, "State should be INFERENCE")
}

func TestNodeWorkGroup_AddRemoveWorkers(t *testing.T) {
	group := NewNodeWorkGroup()

	// Add workers
	node1 := createTestNode("node-1")
	node2 := createTestNode("node-2")

	worker1 := NewNodeWorkerWithClient("node-1", node1, mlnodeclient.NewMockClient())
	worker2 := NewNodeWorkerWithClient("node-2", node2, mlnodeclient.NewMockClient())

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
		worker := NewNodeWorkerWithClient(nodeId, nodes[i], mlnodeclient.NewMockClient())
		group.AddWorker(nodeId, worker)
	}

	// Execute command on all nodes
	var executedCount int32
	var executedNodes sync.Map

	submitted, failed := group.ExecuteOnAll(&TestCommand{
		ExecuteFn: func(worker *NodeWorker) error {
			atomic.AddInt32(&executedCount, 1)
			executedNodes.Store(worker.node.Node.Id, true)
			return nil
		},
	})

	assert.Equal(t, 3, submitted, "Should submit to all 3 nodes")
	assert.Equal(t, 0, failed, "No submissions should fail")
	assert.Equal(t, int32(3), atomic.LoadInt32(&executedCount), "All commands should execute")

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
		worker := NewNodeWorkerWithClient(nodeId, node, mlnodeclient.NewMockClient())
		group.AddWorker(nodeId, worker)
	}

	// Execute on specific nodes only
	nodeCmds := map[string]NodeWorkerCommand{}
	var executedNodes sync.Map
	nodeCmds["node-1"] = &TestCommand{
		ExecuteFn: func(worker *NodeWorker) error {
			executedNodes.Store(worker.node.Node.Id, true)
			return nil
		},
	}
	nodeCmds["node-2"] = &TestCommand{
		ExecuteFn: func(worker *NodeWorker) error {
			executedNodes.Store(worker.node.Node.Id, true)
			return nil
		},
	}

	submitted, failed := group.ExecuteOnNodes(nodeCmds)

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
		worker := NewNodeWorkerWithClient(nodeId, node, mlnodeclient.NewMockClient())
		group.AddWorker(nodeId, worker)
	}

	// Track execution timing
	startTimes := sync.Map{}
	endTimes := sync.Map{}

	startTime := time.Now()

	submitted, failed := group.ExecuteOnAll(&TestCommand{
		ExecuteFn: func(worker *NodeWorker) error {
			startTimes.Store(worker.nodeId, time.Now())
			time.Sleep(50 * time.Millisecond) // Simulate work
			endTimes.Store(worker.nodeId, time.Now())
			return nil
		},
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

	// Concurrently add/remove workers and execute commands
	var wg sync.WaitGroup

	// Worker adder goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			nodeId := fmt.Sprintf("node-%d", i)
			node := createTestNode(nodeId)
			worker := NewNodeWorkerWithClient(nodeId, node, mlnodeclient.NewMockClient())
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

	// Command executor goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			time.Sleep(20 * time.Millisecond)
			group.ExecuteOnAll(&TestCommand{
				ExecuteFn: func(worker *NodeWorker) error {
					// Simple command
					return nil
				},
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
		worker := NewNodeWorkerWithClient(nodeId, node, mlnodeclient.NewMockClient())
		group.AddWorker(nodeId, worker)
	}

	// Execute commands where some fail
	var successCount int32
	var errorCount int32

	submitted, failed := group.ExecuteOnAll(&TestCommand{
		ExecuteFn: func(worker *NodeWorker) error {
			if worker.nodeId == "node-1" {
				atomic.AddInt32(&errorCount, 1)
				return errors.New("simulated error")
			}
			atomic.AddInt32(&successCount, 1)
			return nil
		},
	})

	assert.Equal(t, 3, submitted)
	assert.Equal(t, 0, failed) // Failed means couldn't submit, not command error
	assert.Equal(t, int32(2), atomic.LoadInt32(&successCount), "2 commands should succeed")
	assert.Equal(t, int32(1), atomic.LoadInt32(&errorCount), "1 command should error")
}

// TestCommand is a simple command for testing
type TestCommand struct {
	ExecuteFn func(worker *NodeWorker) error
}

func (c *TestCommand) Execute(worker *NodeWorker) error {
	if c.ExecuteFn != nil {
		return c.ExecuteFn(worker)
	}
	return nil
}
