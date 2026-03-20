package keeper_test

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMsgServer_DeleteGovernanceModel_DoesNotAffectCurrentEpochSnapshot(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	epochIndex := uint64(1)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epochIndex))

	modelID := "model1"
	model := types.Model{
		ProposedBy:             k.GetAuthority(),
		Id:                     modelID,
		UnitsOfComputePerToken: 1,
	}
	k.SetModel(ctx, &model)

	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:     epochIndex,
		ModelId:        "",
		SubGroupModels: []string{modelID},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:     epochIndex,
		ModelId:        modelID,
		ModelSnapshot:  &model,
		SubGroupModels: nil,
	})

	currentGroup, err := k.GetCurrentEpochGroup(ctx)
	require.NoError(t, err)
	subGroup, err := currentGroup.GetSubGroup(ctx, modelID)
	require.NoError(t, err)
	require.NotNil(t, subGroup.GroupData)
	require.NotNil(t, subGroup.GroupData.ModelSnapshot)

	_, err = ms.DeleteGovernanceModel(ctx, &types.MsgDeleteGovernanceModel{
		Authority: k.GetAuthority(),
		Id:        modelID,
	})
	require.NoError(t, err)

	_, found := k.GetGovernanceModel(ctx, modelID)
	require.False(t, found)

	currentGroup, err = k.GetCurrentEpochGroup(ctx)
	require.NoError(t, err)
	subGroup, err = currentGroup.GetSubGroup(ctx, modelID)
	require.NoError(t, err)
	require.NotNil(t, subGroup.GroupData)
	require.NotNil(t, subGroup.GroupData.ModelSnapshot)

	snapshot, err := k.GetEpochModel(ctx, modelID)
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.Equal(t, modelID, snapshot.Id)
}
