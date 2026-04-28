package inference_test

import (
	"context"
	"testing"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

type noopUpgradeKeeper struct{}

func (noopUpgradeKeeper) GetUpgradePlan(ctx context.Context) (upgradetypes.Plan, error) {
	return upgradetypes.Plan{}, nil
}

func TestEndBlock_ProcessesPendingInferenceValidationQueue(t *testing.T) {
	k, ctx := keepertest.InferenceKeeperWithUpgradeKeeper(t, noopUpgradeKeeper{})

	params := types.DefaultParams()
	params.ConfirmationPocParams.ExpectedConfirmationsPerEpoch = 0
	k.SetParams(ctx, params)

	effectiveEpoch := types.Epoch{
		Index:               1,
		PocStartBlockHeight: 1000,
	}
	require.NoError(t, k.SetEpoch(ctx, &effectiveEpoch))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, effectiveEpoch.Index))

	executorAddress := sample.AccAddress()
	modelID := "task-iii-model"
	mainGroupData := types.EpochGroupData{
		EpochIndex:            effectiveEpoch.Index,
		ModelId:               "",
		PocStartBlockHeight:   uint64(effectiveEpoch.PocStartBlockHeight),
		NumberOfRequests:      5,
		PreviousEpochRequests: 3,
		SubGroupModels:        []string{modelID},
		ValidationWeights: []*types.ValidationWeight{
			{
				MemberAddress: executorAddress,
				Weight:        42,
				Reputation:    99,
			},
		},
	}
	k.SetEpochGroupData(ctx, mainGroupData)

	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:          effectiveEpoch.Index,
		ModelId:             modelID,
		PocStartBlockHeight: uint64(effectiveEpoch.PocStartBlockHeight),
		TotalWeight:         100,
	})

	inferenceID := "task-iii-inference"
	require.NoError(t, k.SetInference(ctx, types.Inference{
		Index:       inferenceID,
		InferenceId: inferenceID,
		RequestedBy: sample.AccAddress(),
		ExecutedBy:  executorAddress,
		Model:       modelID,
	}))
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	ctx = ctx.WithBlockHeight(123)
	require.NoError(t, k.EnqueueFinishedInference(ctx, inferenceID))
	require.NoError(t, k.EnqueueFinishedInference(ctx, "missing-inference"))

	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)
	require.NoError(t, am.EndBlock(ctx))

	// The list does not empty after the endblock. TransientStore is wiped at commit, so the list will remain
	// even after EndBlock
	ds, err := k.ListFinishedInferenceIDs(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, ds)

	details, found := k.GetInferenceValidationDetails(ctx, effectiveEpoch.Index, inferenceID)
	require.True(t, found)
	require.Equal(t, inferenceID, details.InferenceId)
	require.Equal(t, modelID, details.Model)
	require.Equal(t, executorAddress, details.ExecutorId)
	require.Equal(t, uint64(42), details.ExecutorPower)
	require.Equal(t, int32(99), details.ExecutorReputation)
	require.Equal(t, uint64(6), details.TrafficBasis)

	updatedMainGroupData, found := k.GetEpochGroupData(ctx, effectiveEpoch.Index, "")
	require.True(t, found)
	require.Equal(t, int64(6), updatedMainGroupData.NumberOfRequests)
}
