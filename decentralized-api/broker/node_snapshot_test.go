package broker

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestGetNodesSnapshotRetainsEpochDataAcrossUpdates(t *testing.T) {
	node := createTestNode("node1")
	node.State.EpochModels["model1"] = types.Model{Id: "model1", HfCommit: "original"}
	node.State.EpochMLNodes["model1"] = types.MLNodeInfo{NodeId: "node1", Throughput: 1}
	node.State.PreservedModels = map[string]bool{"model1": true}
	broker := &Broker{nodes: map[string]*NodeWithState{"node1": node}}
	command := NewGetNodesCommand()
	command.Execute(broker)
	snapshot := <-command.Response
	require.Len(t, snapshot, 1)

	broker.UpdateNodeEpochData([]*types.MLNodeInfo{{NodeId: "node1", Throughput: 2}},
		"model1", types.Model{Id: "model1", HfCommit: "updated"})
	broker.mu.Lock()
	delete(node.State.PreservedModels, "model1")
	broker.mu.Unlock()

	// Status queries must see a stable response after the broker updates its
	// live epoch maps. Their reads happen outside the broker's mutex.
	require.Equal(t, "original", snapshot[0].State.EpochModels["model1"].HfCommit)
	require.Equal(t, int64(1), snapshot[0].State.EpochMLNodes["model1"].Throughput)
	require.True(t, snapshot[0].State.PreservedModels["model1"])

	delete(snapshot[0].State.EpochModels, "model1")
	delete(snapshot[0].State.EpochMLNodes, "model1")
	broker.mu.RLock()
	defer broker.mu.RUnlock()
	require.Equal(t, "updated", node.State.EpochModels["model1"].HfCommit)
	require.Equal(t, int64(2), node.State.EpochMLNodes["model1"].Throughput)
}
