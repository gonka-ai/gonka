package broker

import (
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// GetNodes hands out snapshots that nodeStatusQueryWorker reads without b.mu,
// so a later epoch update must not write into the snapshot's maps.
func TestGetNodesCommand_EpochMapsAreNotShared(t *testing.T) {
	live := &NodeWithState{
		Node: Node{Id: "node-1"},
		State: NodeState{
			EpochModels:     map[string]types.Model{"m1": {Id: "m1"}},
			EpochMLNodes:    map[string]types.MLNodeInfo{"m1": {NodeId: "node-1"}},
			PreservedModels: map[string]bool{"m1": true},
		},
	}
	b := &Broker{nodes: map[string]*NodeWithState{"node-1": live}}

	cmd := NewGetNodesCommand()
	cmd.Execute(b)
	nodes := <-cmd.Response
	require.Len(t, nodes, 1)
	snapshot := nodes[0].State

	b.UpdateNodeEpochData([]*types.MLNodeInfo{{NodeId: "node-1"}}, "m2", types.Model{Id: "m2"})
	b.mu.Lock()
	live.State.PreservedModels["m2"] = true
	b.mu.Unlock()

	require.Contains(t, live.State.EpochModels, "m2")
	require.NotContains(t, snapshot.EpochModels, "m2")
	require.NotContains(t, snapshot.EpochMLNodes, "m2")
	require.NotContains(t, snapshot.PreservedModels, "m2")
	require.Equal(t, types.Model{Id: "m1"}, snapshot.EpochModels["m1"])
}

// reconcile copies the node state under b.mu and getCommandForState reads the
// copy's EpochMLNodes after b.mu is released, so the copy needs its own maps.
func TestCloneEpochMaps_ReconcileCopyIsNotShared(t *testing.T) {
	b, live := epochMapsTestBroker()

	b.mu.Lock()
	stateForCmd := live.State
	cloneEpochMaps(&stateForCmd)
	b.mu.Unlock()

	b.UpdateNodeEpochData([]*types.MLNodeInfo{{NodeId: "node-1"}}, "m2", types.Model{Id: "m2"})
	b.mu.Lock()
	live.State.PreservedModels["m2"] = true
	b.mu.Unlock()

	require.Contains(t, live.State.EpochMLNodes, "m2")
	require.NotContains(t, stateForCmd.EpochModels, "m2")
	require.NotContains(t, stateForCmd.EpochMLNodes, "m2")
	require.NotContains(t, stateForCmd.PreservedModels, "m2")
	require.Equal(t, types.MLNodeInfo{NodeId: "node-1"}, stateForCmd.EpochMLNodes["m1"])
}

// RegisterNode gives the NodeWorker the same *NodeWithState it stores in
// b.nodes, and selectInferenceModel runs on the worker goroutine without b.mu.
// It must not read the epoch maps while UpdateNodeEpochData writes them. Under
// -race the detector reports the pair; without -race the runtime can stop the
// process with "concurrent map read and map write".
func TestSelectInferenceModel_ConcurrentEpochUpdate(t *testing.T) {
	b, live := epochMapsTestBroker()
	w := &NodeWorker{nodeId: "node-1", node: live, broker: b}

	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(prev)

	const rounds = 2000
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		mlNodes := []*types.MLNodeInfo{{NodeId: "node-1"}}
		for i := 0; i < rounds; i++ {
			b.UpdateNodeEpochData(mlNodes, "m1", types.Model{Id: "m1"})
		}
	}()
	for i := 0; i < rounds; i++ {
		model, err := selectInferenceModel(w)
		require.NoError(t, err)
		require.Equal(t, "m1", model.Id)
	}
	wg.Wait()
}

func epochMapsTestBroker() (*Broker, *NodeWithState) {
	live := &NodeWithState{
		Node: Node{Id: "node-1", Models: map[string]ModelArgs{"m1": {}}},
		State: NodeState{
			EpochModels:     map[string]types.Model{"m1": {Id: "m1"}},
			EpochMLNodes:    map[string]types.MLNodeInfo{"m1": {NodeId: "node-1"}},
			PreservedModels: map[string]bool{},
		},
	}
	return &Broker{nodes: map[string]*NodeWithState{"node-1": live}}, live
}

// A nil map stays nil in the snapshot.
func TestGetNodesCommand_NilEpochMapsStayNil(t *testing.T) {
	b := &Broker{nodes: map[string]*NodeWithState{"node-1": {Node: Node{Id: "node-1"}}}}
	cmd := NewGetNodesCommand()
	cmd.Execute(b)
	nodes := <-cmd.Response
	require.Len(t, nodes, 1)
	require.Nil(t, nodes[0].State.EpochModels)
	require.Nil(t, nodes[0].State.EpochMLNodes)
	require.Nil(t, nodes[0].State.PreservedModels)
}
