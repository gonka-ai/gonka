package broker

import (
	"context"
	"decentralized-api/chainphase"
	"decentralized-api/mlnodeclient"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCommandLoopBroker() *Broker {
	b := &Broker{
		highPriorityCommands: make(chan Command, 100),
		lowPriorityCommands:  make(chan Command, 100),
		nodes:                make(map[string]*NodeWithState),
		nodeWorkGroup:        NewNodeWorkGroup(),
		lockMap:              make(map[string]lockEntry),
		phaseTracker:         &chainphase.ChainPhaseTracker{},
	}
	go b.processCommands()
	return b
}

func waitChan[T any](t *testing.T, ch <-chan T, d time.Duration, msg string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		t.Fatal(msg)
		var zero T
		return zero
	}
}

func TestRemoveNode_DoesNotBlockBrokerLoop(t *testing.T) {
	broker := newCommandLoopBroker()
	node := createTestNode("node-7")
	worker := NewNodeWorkerWithClient(node.Node.Id, node, mlnodeclient.NewMockClient(), broker)

	broker.mu.Lock()
	broker.nodes[node.Node.Id] = node
	broker.mu.Unlock()
	broker.nodeWorkGroup.AddWorker(node.Node.Id, worker)

	started := make(chan struct{})
	sawCancel := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	node.State.cancelInFlightTask = cancel

	ok := worker.Submit(ctx, &TestCommand{
		ExecuteFn: func(ctx context.Context, worker *NodeWorker) NodeResult {
			close(started)
			<-ctx.Done()
			close(sawCancel)
			return NodeResult{Succeeded: false, Error: ctx.Err().Error()}
		},
	})
	require.True(t, ok)
	waitChan(t, started, 500*time.Millisecond, "command did not start")

	removeResp := make(chan bool, 2)
	require.NoError(t, broker.QueueMessage(RemoveNode{
		NodeId:   node.Node.Id,
		Response: removeResp,
	}))

	waitChan(t, sawCancel, 500*time.Millisecond, "in-flight Execute did not observe cancellation")
	removed := waitChan(t, removeResp, 500*time.Millisecond, "RemoveNode blocked the broker loop")
	assert.True(t, removed)

	_, exists := broker.nodeWorkGroup.GetWorker(node.Node.Id)
	assert.False(t, exists, "worker must be unregistered even if drain is still running")

	lockResp := make(chan *Node, 2)
	require.NoError(t, broker.QueueMessage(LockAvailableNode{
		Model:    "model1",
		Response: lockResp,
	}))
	_ = waitChan(t, lockResp, 500*time.Millisecond, "LockAvailableNode queued after RemoveNode was blocked")
}

func TestNodeWorker_ShutdownWithFullBrokerQueue(t *testing.T) {
	broker := NewTestBroker2(1)
	broker.nodes = make(map[string]*NodeWithState)
	broker.phaseTracker = &chainphase.ChainPhaseTracker{}
	broker.highPriorityCommands <- UpdateNodeResultCommand{
		NodeId:   "filler",
		Response: make(chan bool, 1),
	}

	node := createTestNode("node-7")
	worker := NewNodeWorkerWithClient(node.Node.Id, node, mlnodeclient.NewMockClient(), broker)

	started := make(chan struct{})
	ok := worker.Submit(context.Background(), &TestCommand{
		ExecuteFn: func(ctx context.Context, worker *NodeWorker) NodeResult {
			close(started)
			return NodeResult{Succeeded: true}
		},
	})
	require.True(t, ok)
	waitChan(t, started, 500*time.Millisecond, "command did not start")

	drained := worker.ShutdownWithTimeout(100 * time.Millisecond)
	assert.False(t, drained, "shutdown must time out while QueueMessage is blocked")

	<-broker.highPriorityCommands // filler
	resultCmd := waitChan(t, broker.highPriorityCommands, 500*time.Millisecond, "abandoned result was not delivered")
	update, ok := resultCmd.(UpdateNodeResultCommand)
	require.True(t, ok)
	update.Execute(broker)
	assert.False(t, <-update.Response, "late result for a deleted node must be treated as unknown")
}

func TestNodeWorker_ShutdownWithTimeout_CleanDrain(t *testing.T) {
	broker := NewTestBroker2(4)
	node := createTestNode("node-1")
	worker := NewNodeWorkerWithClient(node.Node.Id, node, mlnodeclient.NewMockClient(), broker)

	var executed int32
	for i := 0; i < 3; i++ {
		ok := worker.Submit(context.Background(), &TestCommand{
			ExecuteFn: func(ctx context.Context, worker *NodeWorker) NodeResult {
				atomic.AddInt32(&executed, 1)
				return NodeResult{Succeeded: true}
			},
		})
		require.True(t, ok)
	}

	assert.True(t, worker.ShutdownWithTimeout(time.Second), "clean drain should succeed")
	assert.Equal(t, int32(3), atomic.LoadInt32(&executed))
}

func TestNodeWorker_ShutdownWithTimeout_ReturnsFalseOnHang(t *testing.T) {
	broker := NewTestBroker2(1)
	node := createTestNode("node-1")
	worker := NewNodeWorkerWithClient(node.Node.Id, node, mlnodeclient.NewMockClient(), broker)

	started := make(chan struct{})
	release := make(chan struct{})
	ok := worker.Submit(context.Background(), &TestCommand{
		ExecuteFn: func(ctx context.Context, worker *NodeWorker) NodeResult {
			close(started)
			<-release
			return NodeResult{Succeeded: true}
		},
	})
	require.True(t, ok)
	waitChan(t, started, 500*time.Millisecond, "command did not start")

	assert.False(t, worker.ShutdownWithTimeout(50*time.Millisecond))
	close(release)
	assert.True(t, worker.ShutdownWithTimeout(time.Second), "second wait should observe drain after release")
}

func TestNodeWorker_ShutdownIdempotent(t *testing.T) {
	broker := NewTestBroker2(1)
	node := createTestNode("node-1")
	worker := NewNodeWorkerWithClient(node.Node.Id, node, mlnodeclient.NewMockClient(), broker)

	worker.Shutdown()
	worker.Shutdown() // must not panic on a second close
	assert.False(t, worker.Submit(context.Background(), &TestCommand{}), "submit after shutdown must be rejected")
}

func TestNodeWorkGroup_RemoveWorkerUnregistersOnTimeout(t *testing.T) {
	group := NewNodeWorkGroup()
	broker := NewTestBroker2(1)
	node := createTestNode("node-1")
	worker := NewNodeWorkerWithClient(node.Node.Id, node, mlnodeclient.NewMockClient(), broker)
	group.AddWorker(node.Node.Id, worker)

	started := make(chan struct{})
	release := make(chan struct{})
	ok := worker.Submit(context.Background(), &TestCommand{
		ExecuteFn: func(ctx context.Context, worker *NodeWorker) NodeResult {
			close(started)
			<-release
			return NodeResult{Succeeded: true}
		},
	})
	require.True(t, ok)
	waitChan(t, started, 500*time.Millisecond, "command did not start")

	group.RemoveWorkerAsync(node.Node.Id)
	_, exists := group.GetWorker(node.Node.Id)
	assert.False(t, exists, "worker must be gone from the group even though drain has not finished")

	close(release)
}

func TestRemoveNode_UnknownId(t *testing.T) {
	broker := newCommandLoopBroker()
	resp := make(chan bool, 2)
	require.NoError(t, broker.QueueMessage(RemoveNode{NodeId: "missing", Response: resp}))
	assert.False(t, waitChan(t, resp, 500*time.Millisecond, "RemoveNode for unknown id did not return"))
}

func TestRemoveNode_NilCancelInFlightTask(t *testing.T) {
	broker := newCommandLoopBroker()
	node := createTestNode("node-1")
	worker := NewNodeWorkerWithClient(node.Node.Id, node, mlnodeclient.NewMockClient(), broker)
	broker.mu.Lock()
	broker.nodes[node.Node.Id] = node
	broker.mu.Unlock()
	broker.nodeWorkGroup.AddWorker(node.Node.Id, worker)

	resp := make(chan bool, 2)
	require.NoError(t, broker.QueueMessage(RemoveNode{NodeId: node.Node.Id, Response: resp}))
	assert.True(t, waitChan(t, resp, 500*time.Millisecond, "RemoveNode with nil cancelInFlightTask did not return"))
	_, exists := broker.nodeWorkGroup.GetWorker(node.Node.Id)
	assert.False(t, exists)
}
