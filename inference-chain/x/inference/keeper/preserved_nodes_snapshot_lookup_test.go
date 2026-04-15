package keeper

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func TestPreservedNodeSetByModel(t *testing.T) {
	snapshot := &types.PreservedNodesSnapshot{
		EpisodeAnchorHeight: 100,
		ModelPreservedNodes: []*types.ModelPreservedNodes{
			{
				ModelId:          "model-a",
				PreservedNodeIds: []string{"node-1", "node-2"},
			},
			{
				ModelId:          "model-b",
				PreservedNodeIds: []string{"node-3"},
			},
		},
	}

	modelANodes := PreservedNodeSetByModel(snapshot, "model-a")
	require.Len(t, modelANodes, 2)
	require.Contains(t, modelANodes, "node-1")
	require.Contains(t, modelANodes, "node-2")

	modelBNodes := PreservedNodeSetByModel(snapshot, "model-b")
	require.Len(t, modelBNodes, 1)
	require.Contains(t, modelBNodes, "node-3")

	require.True(t, IsPreservedNode(snapshot, "model-b", "node-3"))
	require.False(t, IsPreservedNode(snapshot, "model-b", "node-1"))
	require.Empty(t, PreservedNodeSetByModel(snapshot, "missing-model"))
}
