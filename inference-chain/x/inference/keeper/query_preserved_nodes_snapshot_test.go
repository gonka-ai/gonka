package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestPreservedNodesSnapshotQuery(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	snapshot := types.PreservedNodesSnapshot{
		EpisodeAnchorHeight: 100,
		ModelPreservedNodes: []*types.ModelPreservedNodes{
			{
				ModelId: "model-a",
				Participants: []*types.ParticipantPreservedNodes{
					{ParticipantId: "p1", NodeIds: []string{"node-1", "node-2"}},
				},
			},
		},
	}

	require.NoError(t, keeper.SetPreservedNodesSnapshot(ctx, snapshot))

	resp, err := keeper.PreservedNodesSnapshot(ctx, &types.QueryPreservedNodesSnapshotRequest{})
	require.NoError(t, err)
	require.True(t, resp.Found)
	require.Equal(t, &snapshot, resp.Snapshot)
}

func TestPreservedNodesSnapshotQueryNotFound(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	resp, err := keeper.PreservedNodesSnapshot(ctx, &types.QueryPreservedNodesSnapshotRequest{})
	require.NoError(t, err)
	require.False(t, resp.Found)
	require.Nil(t, resp.Snapshot)
}

func TestPreservedNodesSnapshotQueryInvalidRequest(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	_, err := keeper.PreservedNodesSnapshot(ctx, nil)
	require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "invalid request"))
}

func TestPreservedNodesSnapshotOverwrite(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	first := types.PreservedNodesSnapshot{
		EpisodeAnchorHeight: 200,
		ModelPreservedNodes: []*types.ModelPreservedNodes{
			{
				ModelId: "model-b",
				Participants: []*types.ParticipantPreservedNodes{
					{ParticipantId: "p1", NodeIds: []string{"node-3"}},
				},
			},
		},
	}
	require.NoError(t, keeper.SetPreservedNodesSnapshot(ctx, first))

	second := types.PreservedNodesSnapshot{
		EpisodeAnchorHeight: 300,
		ModelPreservedNodes: []*types.ModelPreservedNodes{
			{
				ModelId: "model-b",
				Participants: []*types.ParticipantPreservedNodes{
					{ParticipantId: "p1", NodeIds: []string{"node-4"}},
				},
			},
		},
	}
	require.NoError(t, keeper.SetPreservedNodesSnapshot(ctx, second))

	stored, found, err := keeper.GetPreservedNodesSnapshot(ctx)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, second, stored)
}
