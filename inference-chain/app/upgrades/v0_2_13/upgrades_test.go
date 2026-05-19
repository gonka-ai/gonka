package v0_2_13

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.13", UpgradeName)
}

func TestSetDevshardEscrowParams_BackfillsDefaultSealGraceNonces(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, params.DevshardEscrowParams)
	// Simulate pre-v0.2.13 state where the new field is zero on disk.
	params.DevshardEscrowParams.DefaultSealGraceNonces = 0
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, setDevshardEscrowParams(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.DevshardEscrowParams)
	expected := inferencetypes.DefaultDevshardSealGraceNonces(got.DevshardEscrowParams.GroupSize)
	require.Equal(t, expected, got.DevshardEscrowParams.DefaultSealGraceNonces)
	require.Equal(t, MaxEscrowsPerEpoch, got.DevshardEscrowParams.MaxEscrowsPerEpoch)
	require.Equal(t, MaxNonce, got.DevshardEscrowParams.MaxNonce)
}

func TestSetDevshardEscrowParams_PreservesExistingDefaultSealGraceNonces(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, params.DevshardEscrowParams)
	const customGrace uint32 = 12345
	params.DevshardEscrowParams.DefaultSealGraceNonces = customGrace
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, setDevshardEscrowParams(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, customGrace, got.DevshardEscrowParams.DefaultSealGraceNonces)
}

func TestBackfillConfirmationWeightScales(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{
		{ModelId: "model-a", WeightScaleFactor: inferencetypes.DecimalFromFloat(2)},
		{ModelId: "model-b", WeightScaleFactor: inferencetypes.DecimalFromFloat(3)},
	}
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 7))

	alice := sample.AccAddress()
	bob := sample.AccAddress()

	require.NoError(t, k.SetActiveParticipants(ctx, inferencetypes.ActiveParticipants{
		EpochId: 7,
		Participants: []*inferencetypes.ActiveParticipant{
			{
				Index:  alice,
				Models: []string{"model-a", "model-b", "model-c"},
				MlNodes: []*inferencetypes.ModelMLNodes{
					{MlNodes: []*inferencetypes.MLNodeInfo{{PocWeight: 10}}},
					{MlNodes: []*inferencetypes.MLNodeInfo{{PocWeight: 20}}},
					{MlNodes: []*inferencetypes.MLNodeInfo{{PocWeight: 30}}},
				},
			},
			{
				Index:  bob,
				Models: []string{"model-a"},
				MlNodes: []*inferencetypes.ModelMLNodes{
					{MlNodes: []*inferencetypes.MLNodeInfo{{PocWeight: 5}}},
				},
			},
		},
	}))

	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 7,
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: alice, Weight: 100, ConfirmationWeight: 999},
			{MemberAddress: bob, Weight: 50, ConfirmationWeight: 5},
		},
	})
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 7,
		ModelId:    "model-a",
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: alice, VotingPower: 100},
		},
	})
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 7,
		ModelId:    "model-b",
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: alice, VotingPower: 0},
		},
	})
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 7,
		ModelId:    "model-c",
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: alice, VotingPower: 1},
		},
	})
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 8,
		ModelId:    "model-d",
		ValidationWeights: []*inferencetypes.ValidationWeight{
			{MemberAddress: alice, VotingPower: 1},
		},
	})

	require.NoError(t, backfillConfirmationWeightScales(ctx, k))

	root, found := k.GetEpochGroupData(ctx, 7, "")
	require.True(t, found)
	require.Len(t, root.ConfirmationWeightScales, 2)
	require.Equal(t, "model-a", root.ConfirmationWeightScales[0].ModelId)
	require.True(t, root.ConfirmationWeightScales[0].WeightScaleFactor.ToDecimal().Equal(inferencetypes.DecimalFromFloat(2).ToDecimal()))
	require.Equal(t, "model-c", root.ConfirmationWeightScales[1].ModelId)
	require.True(t, root.ConfirmationWeightScales[1].WeightScaleFactor.ToDecimal().Equal(inferencetypes.DecimalFromFloat(1).ToDecimal()))

	require.Equal(t, int64(50), root.ValidationWeights[0].ConfirmationWeight)
	require.Equal(t, int64(5), root.ValidationWeights[1].ConfirmationWeight)
}
