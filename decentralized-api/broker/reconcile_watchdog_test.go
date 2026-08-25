package broker

import (
	"context"
	"decentralized-api/chainphase"
	"decentralized-api/mlnodeclient"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconcileInfo_isExpired(t *testing.T) {
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)

	assert.False(t, (*ReconcileInfo)(nil).isExpired(now))
	assert.True(t, (&ReconcileInfo{}).isExpired(now), "zero StartedAt is stale")
	assert.False(t, (&ReconcileInfo{StartedAt: now}).isExpired(now))
	assert.False(t, (&ReconcileInfo{StartedAt: now.Add(-staleReconcileTimeout + time.Second)}).isExpired(now))
	assert.True(t, (&ReconcileInfo{StartedAt: now.Add(-staleReconcileTimeout)}).isExpired(now))
	assert.True(t, (&ReconcileInfo{StartedAt: now.Add(-staleReconcileTimeout - time.Second)}).isExpired(now))
}

func syncedEpochState() chainphase.EpochState {
	return chainphase.EpochState{
		CurrentBlock: chainphase.BlockInfo{Height: 1, Hash: "hash-1"},
		IsSynced:     true,
	}
}

func newWatchdogBroker() *Broker {
	return &Broker{
		highPriorityCommands: make(chan Command, 20),
		lowPriorityCommands:  make(chan Command, 20),
		nodes:                make(map[string]*NodeWithState),
		nodeWorkGroup:        NewNodeWorkGroup(),
	}
}

func attachWorker(t *testing.T, b *Broker, node *NodeWithState) *NodeWorker {
	t.Helper()
	worker := NewNodeWorkerWithClient(node.Node.Id, node, mlnodeclient.NewMockClient(), b)
	t.Cleanup(worker.Shutdown)
	b.nodeWorkGroup.AddWorker(node.Node.Id, worker)
	return worker
}

// nodeStuckStopping is away from its intended STOPPED status so phase 2
// dispatches StopNodeCommand (no chainBridge / epoch models required).
func nodeStuckStopping(id string) *NodeWithState {
	node := createTestNode(id)
	node.State.CurrentStatus = types.HardwareNodeStatus_INFERENCE
	node.State.IntendedStatus = types.HardwareNodeStatus_STOPPED
	node.State.PocCurrentStatus = PocStatusIdle
	node.State.PocIntendedStatus = PocStatusIdle
	return node
}

func stoppingReconcileInfo(startedAt time.Time) *ReconcileInfo {
	return &ReconcileInfo{
		Status:    types.HardwareNodeStatus_STOPPED,
		PocStatus: PocStatusIdle,
		StartedAt: startedAt,
	}
}

func TestReconcile_AbortsStaleReconcileInfoAndRetries(t *testing.T) {
	broker := newWatchdogBroker()
	node := nodeStuckStopping("node-stale")
	cancelled := make(chan struct{})
	node.State.cancelInFlightTask = func() { close(cancelled) }
	node.State.ReconcileInfo = stoppingReconcileInfo(time.Now().Add(-staleReconcileTimeout - time.Second))
	broker.nodes[node.Node.Id] = node
	attachWorker(t, broker, node)

	broker.reconcile(syncedEpochState())

	select {
	case <-cancelled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("stale in-flight reconcile was not cancelled")
	}

	broker.mu.Lock()
	info := node.State.ReconcileInfo
	broker.mu.Unlock()
	require.NotNil(t, info, "phase 2 should dispatch a new attempt after aborting the stale one")
	assert.Equal(t, types.HardwareNodeStatus_STOPPED, info.Status)
	assert.False(t, info.StartedAt.IsZero())
	assert.WithinDuration(t, time.Now(), info.StartedAt, 2*time.Second)
}

func TestReconcile_LeavesFreshReconcileInfoAlone(t *testing.T) {
	broker := newWatchdogBroker()
	node := nodeStuckStopping("node-fresh")
	started := time.Now().Add(-time.Second)
	cancelCalled := false
	node.State.cancelInFlightTask = func() { cancelCalled = true }
	node.State.ReconcileInfo = stoppingReconcileInfo(started)
	broker.nodes[node.Node.Id] = node
	attachWorker(t, broker, node)

	broker.reconcile(syncedEpochState())

	assert.False(t, cancelCalled)
	broker.mu.Lock()
	info := node.State.ReconcileInfo
	broker.mu.Unlock()
	require.NotNil(t, info)
	assert.Equal(t, started, info.StartedAt)
}

func TestReconcile_ZeroStartedAtIsStale(t *testing.T) {
	broker := newWatchdogBroker()
	node := nodeStuckStopping("node-legacy")
	node.State.ReconcileInfo = stoppingReconcileInfo(time.Time{})
	broker.nodes[node.Node.Id] = node
	attachWorker(t, broker, node)

	broker.reconcile(syncedEpochState())

	broker.mu.Lock()
	info := node.State.ReconcileInfo
	broker.mu.Unlock()
	require.NotNil(t, info)
	assert.False(t, info.StartedAt.IsZero(), "legacy zero StartedAt must be expired so a new attempt can start")
}

func TestReconcile_StaleStillCancelsWhenCancelFuncMissing(t *testing.T) {
	broker := newWatchdogBroker()
	node := nodeStuckStopping("node-no-cancel")
	node.State.ReconcileInfo = stoppingReconcileInfo(time.Now().Add(-staleReconcileTimeout - time.Second))
	broker.nodes[node.Node.Id] = node
	attachWorker(t, broker, node)

	broker.reconcile(syncedEpochState())

	broker.mu.Lock()
	info := node.State.ReconcileInfo
	broker.mu.Unlock()
	require.NotNil(t, info)
	assert.WithinDuration(t, time.Now(), info.StartedAt, 2*time.Second)
}

func TestReconcile_BlockingExecuteObservesWatchdogCancel(t *testing.T) {
	broker := newWatchdogBroker()
	node := nodeStuckStopping("node-block")
	broker.nodes[node.Node.Id] = node
	worker := attachWorker(t, broker, node)

	started := make(chan struct{})
	sawCancel := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	node.State.cancelInFlightTask = cancel
	node.State.ReconcileInfo = stoppingReconcileInfo(time.Now().Add(-staleReconcileTimeout - time.Second))

	ok := worker.Submit(ctx, &TestCommand{
		ExecuteFn: func(ctx context.Context, worker *NodeWorker) NodeResult {
			close(started)
			<-ctx.Done()
			close(sawCancel)
			return NodeResult{Succeeded: false, Error: ctx.Err().Error()}
		},
	})
	require.True(t, ok)
	select {
	case <-started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("blocking command did not start")
	}

	broker.reconcile(syncedEpochState())

	select {
	case <-sawCancel:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("blocking Execute did not observe watchdog cancellation")
	}
}
