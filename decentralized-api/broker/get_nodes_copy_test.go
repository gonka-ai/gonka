package broker

import (
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
