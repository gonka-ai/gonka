package inference

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMergeByModel_BothEmpty(t *testing.T) {
	models, nodes := mergeByModel(nil, nil, nil, nil)
	require.Empty(t, models)
	require.Empty(t, nodes)
}

func TestMergeByModel_PreservedOnly(t *testing.T) {
	preservedModels := []string{"model-a"}
	preservedNodes := []*types.ModelMLNodes{
		{MlNodes: []*types.MLNodeInfo{{NodeId: "n1", PocWeight: 100}}},
	}

	models, nodes := mergeByModel(preservedModels, preservedNodes, nil, nil)
	require.Equal(t, []string{"model-a"}, models)
	require.Len(t, nodes, 1)
	require.Equal(t, int64(100), nodes[0].MlNodes[0].PocWeight)
}

func TestMergeByModel_PocOnly(t *testing.T) {
	pocModels := []string{"model-b"}
	pocNodes := []*types.ModelMLNodes{
		{MlNodes: []*types.MLNodeInfo{{NodeId: "n2", PocWeight: 200}}},
	}

	models, nodes := mergeByModel(nil, nil, pocModels, pocNodes)
	require.Equal(t, []string{"model-b"}, models)
	require.Len(t, nodes, 1)
	require.Equal(t, int64(200), nodes[0].MlNodes[0].PocWeight)
}

func TestMergeByModel_OverlappingAndDisjoint(t *testing.T) {
	// preserved: model-a (n1), model-b (n2)
	// poc:       model-a (n3), model-c (n4)
	// expected:  model-a (n1, n3), model-b (n2), model-c (n4)
	preservedModels := []string{"model-a", "model-b"}
	preservedNodes := []*types.ModelMLNodes{
		{MlNodes: []*types.MLNodeInfo{{NodeId: "n1", PocWeight: 100}}},
		{MlNodes: []*types.MLNodeInfo{{NodeId: "n2", PocWeight: 200}}},
	}
	pocModels := []string{"model-a", "model-c"}
	pocNodes := []*types.ModelMLNodes{
		{MlNodes: []*types.MLNodeInfo{{NodeId: "n3", PocWeight: 150}}},
		{MlNodes: []*types.MLNodeInfo{{NodeId: "n4", PocWeight: 50}}},
	}

	models, nodes := mergeByModel(preservedModels, preservedNodes, pocModels, pocNodes)

	require.Equal(t, []string{"model-a", "model-b", "model-c"}, models)
	require.Len(t, nodes, 3)

	// model-a: n1 + n3
	require.Len(t, nodes[0].MlNodes, 2)
	require.Equal(t, "n1", nodes[0].MlNodes[0].NodeId)
	require.Equal(t, "n3", nodes[0].MlNodes[1].NodeId)

	// model-b: n2
	require.Len(t, nodes[1].MlNodes, 1)
	require.Equal(t, "n2", nodes[1].MlNodes[0].NodeId)

	// model-c: n4
	require.Len(t, nodes[2].MlNodes, 1)
	require.Equal(t, "n4", nodes[2].MlNodes[0].NodeId)
}

func TestMergeByModel_DeduplicatesByNodeId(t *testing.T) {
	// Same node in both sides for same model -- should appear once
	preservedModels := []string{"model-a"}
	preservedNodes := []*types.ModelMLNodes{
		{MlNodes: []*types.MLNodeInfo{{NodeId: "n1", PocWeight: 100}}},
	}
	pocModels := []string{"model-a"}
	pocNodes := []*types.ModelMLNodes{
		{MlNodes: []*types.MLNodeInfo{{NodeId: "n1", PocWeight: 150}}},
	}

	models, nodes := mergeByModel(preservedModels, preservedNodes, pocModels, pocNodes)

	require.Equal(t, []string{"model-a"}, models)
	require.Len(t, nodes[0].MlNodes, 1)
	// Preserved node wins (added first)
	require.Equal(t, int64(100), nodes[0].MlNodes[0].PocWeight)
}

func TestMergeByModel_FiltersEmptyNodeIds(t *testing.T) {
	preservedModels := []string{"model-a"}
	preservedNodes := []*types.ModelMLNodes{
		{MlNodes: []*types.MLNodeInfo{
			{NodeId: "n1", PocWeight: 100},
			{NodeId: "", PocWeight: 50},
		}},
	}

	models, nodes := mergeByModel(preservedModels, preservedNodes, nil, nil)
	require.Equal(t, []string{"model-a"}, models)
	require.Len(t, nodes[0].MlNodes, 1)
	require.Equal(t, "n1", nodes[0].MlNodes[0].NodeId)
}
