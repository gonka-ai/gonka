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
				ModelId: "model-a",
				Participants: []*types.ParticipantPreservedNodes{
					{ParticipantId: "p1", NodeIds: []string{"node-1", "node-2"}},
					{ParticipantId: "p2", NodeIds: []string{"node-1"}},
				},
			},
			{
				ModelId: "model-b",
				Participants: []*types.ParticipantPreservedNodes{
					{ParticipantId: "p1", NodeIds: []string{"node-3"}},
				},
			},
		},
	}

	modelANodes := PreservedNodeSetByModel(snapshot, "model-a")
	require.Len(t, modelANodes, 2)
	require.True(t, IsPreservedNode(modelANodes, "p1", "node-1"))
	require.True(t, IsPreservedNode(modelANodes, "p1", "node-2"))
	require.True(t, IsPreservedNode(modelANodes, "p2", "node-1"))
	require.False(t, IsPreservedNode(modelANodes, "p2", "node-2"))

	modelBNodes := PreservedNodeSetByModel(snapshot, "model-b")
	require.Len(t, modelBNodes, 1)
	require.True(t, IsPreservedNode(modelBNodes, "p1", "node-3"))
	require.False(t, IsPreservedNode(modelBNodes, "p2", "node-3"))

	require.Empty(t, PreservedNodeSetByModel(snapshot, "missing-model"))
	require.Empty(t, PreservedNodeSetByModel(nil, "model-a"))
	require.False(t, IsPreservedNode(nil, "p1", "node-1"))
}
