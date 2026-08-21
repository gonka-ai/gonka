package inference

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/types"
)

func valOperOf(t *testing.T, accBech32 string) string {
	t.Helper()
	acc, err := sdk.AccAddressFromBech32(accBech32)
	require.NoError(t, err)
	return sdk.ValAddress(acc).String()
}

func TestApplyPreviousConfirmedWeightCap_ClampsAndZeroes(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	const prevEpoch = uint64(5)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, prevEpoch))

	// Previous epoch root group:
	//   Validator  : consensus weight 100, fully confirmed  -> cap 100
	//   Validator2 : consensus weight 200, half confirmed   -> cap 100
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:     prevEpoch,
		ModelId:        "",
		SubGroupModels: []string{"model-a"},
		ConfirmationWeightScales: []*types.ConfirmationWeightScale{
			{ModelId: "model-a", WeightScaleFactor: types.DecimalFromFloat(1)},
		},
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Validator, Weight: 100, ConfirmationWeight: 50},
			{MemberAddress: testutil.Validator2, Weight: 200, ConfirmationWeight: 50},
		},
	})
	// Subgroup carries the raw per-node PoC weights (rawConfirmationTotal).
	//   Validator  : raw 50, confirmed 50 -> fully confirmed  -> cap 100
	//   Validator2 : raw 100, confirmed 50 -> half confirmed  -> cap 200*50/100 = 100
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: prevEpoch,
		ModelId:    "model-a",
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Validator, MlNodes: []*types.MLNodeInfo{{PocWeight: 50}}},
			{MemberAddress: testutil.Validator2, MlNodes: []*types.MLNodeInfo{{PocWeight: 100}}},
		},
	})

	am := NewAppModule(nil, k, nil, nil, nil, nil)

	participants := []*types.ActiveParticipant{
		// Fully-confirmed last epoch, unchanged -> stays 100.
		{Index: testutil.Validator, Weight: 100},
		// Only half-confirmed last epoch but tries to jump to 500 -> clamped to 100.
		{Index: testutil.Validator2, Weight: 500},
		// Brand new participant -> zeroed.
		{Index: testutil.Executor, Weight: 300},
	}

	result := am.applyPreviousConfirmedWeightCap(ctx, participants)

	cap := map[string]int64{}
	weight := map[string]int64{}
	for _, p := range result {
		cap[p.Index] = p.CapWeight
		weight[p.Index] = p.Weight
	}
	// CapWeight is capped; Weight (real, for rewards) is preserved.
	require.Equal(t, int64(100), cap[testutil.Validator], "unchanged fully-confirmed participant")
	require.Equal(t, int64(100), cap[testutil.Validator2], "clamped to previous confirmed weight")
	require.Equal(t, int64(0), cap[testutil.Executor], "new participant zeroed")
	require.Equal(t, int64(100), weight[testutil.Validator], "real weight preserved")
	require.Equal(t, int64(500), weight[testutil.Validator2], "real weight preserved")
	require.Equal(t, int64(300), weight[testutil.Executor], "real weight preserved for rewards")
}

func TestApplyPreviousConfirmedWeightCap_GuardianKeepsRealWeight(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	const prevEpoch = uint64(5)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, prevEpoch))
	guardianOperator := valOperOf(t, testutil.Validator2)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.GenesisGuardianParams = &types.GenesisGuardianParams{
		GuardianAddresses: []string{guardianOperator},
	}
	require.NoError(t, k.SetParams(ctx, params))

	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: prevEpoch,
		ModelId:    "",
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Validator, Weight: 100},
			{MemberAddress: testutil.Validator2, Weight: 100},
		},
	})

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	result := am.applyPreviousConfirmedWeightCap(ctx, []*types.ActiveParticipant{
		{Index: testutil.Validator, Weight: 500},
		{Index: testutil.Validator2, Weight: 500},
	})

	require.Equal(t, int64(100), result[0].CapWeight, "regular participant is capped")
	require.Equal(t, int64(500), result[1].CapWeight, "guardian keeps real weight")
}

func TestApplyPreviousConfirmedWeightCap_OnlyLivePreviousMembersProvideCap(t *testing.T) {
	k, ctx, groupStub := newMinimalInferenceKeeperWithStub(t)

	const prevEpoch = uint64(5)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, prevEpoch))
	groupStub.excludedMembers = map[string]bool{testutil.Validator2: true}

	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   prevEpoch,
		ModelId:      "",
		EpochGroupId: 77,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Validator, Weight: 100},
			{MemberAddress: testutil.Validator2, Weight: 100},
		},
	})

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	result := am.applyPreviousConfirmedWeightCap(ctx, []*types.ActiveParticipant{
		{Index: testutil.Validator, Weight: 100},
		{Index: testutil.Validator2, Weight: 100},
	})

	require.Equal(t, int64(100), result[0].CapWeight, "live previous member keeps cap")
	require.Equal(t, int64(0), result[1].CapWeight, "removed previous member is treated as absent")
}

func TestApplyPreviousConfirmedWeightCap_NoScalesUsesConsensusWeight(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	const prevEpoch = uint64(3)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, prevEpoch))

	// No confirmation weight scales: cap is the previous consensus weight itself.
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: prevEpoch,
		ModelId:    "",
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Validator, Weight: 120, ConfirmationWeight: 10},
		},
	})

	am := NewAppModule(nil, k, nil, nil, nil, nil)

	participants := []*types.ActiveParticipant{
		{Index: testutil.Validator, Weight: 999},
	}
	result := am.applyPreviousConfirmedWeightCap(ctx, participants)
	require.Equal(t, int64(120), result[0].CapWeight, "cap weight clamped to previous consensus weight")
	require.Equal(t, int64(999), result[0].Weight, "real weight preserved")
}

func TestApplyPreviousConfirmedWeightCap_BootstrapSkips(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	// No effective epoch index set -> bootstrap, weights untouched.
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	participants := []*types.ActiveParticipant{
		{Index: testutil.Validator, Weight: 100},
		{Index: testutil.Executor, Weight: 300},
	}
	result := am.applyPreviousConfirmedWeightCap(ctx, participants)
	// Bootstrap: CapWeight defaults to Weight (no capping), Weight untouched.
	require.Equal(t, int64(100), result[0].CapWeight)
	require.Equal(t, int64(300), result[1].CapWeight)
	require.Equal(t, int64(100), result[0].Weight)
	require.Equal(t, int64(300), result[1].Weight)
}

func TestApplyPreviousConfirmedWeightCap_MissingPrevGroupSkips(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	// Effective epoch set but no root group data for it -> skip rather than zero.
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 7))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	participants := []*types.ActiveParticipant{
		{Index: testutil.Executor, Weight: 300},
	}
	result := am.applyPreviousConfirmedWeightCap(ctx, participants)
	// Missing prev group: skip capping, CapWeight defaults to Weight.
	require.Equal(t, int64(300), result[0].CapWeight)
	require.Equal(t, int64(300), result[0].Weight)
}

func TestResolveTrustWeights_UsesCapWhenApplied(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: testutil.Validator, Weight: 100, CapWeight: 80},
		{Index: testutil.Validator2, Weight: 200, CapWeight: 200},
		{Index: testutil.Executor, Weight: 300, CapWeight: 0}, // new participant
	}
	weights := resolveTrustWeights(participants)
	require.Equal(t, int64(80), weights[testutil.Validator])
	require.Equal(t, int64(200), weights[testutil.Validator2])
	require.Equal(t, int64(0), weights[testutil.Executor], "new participant contributes zero trust weight")
}

func TestResolveTrustWeights_FallsBackToWeightWhenCapUnset(t *testing.T) {
	// No CapWeight populated anywhere (e.g. participants built without running the
	// cap, or a pre-upgrade epoch): fall back to real Weight so we never collapse
	// to all-zero trust weights.
	participants := []*types.ActiveParticipant{
		{Index: testutil.Validator, Weight: 100},
		{Index: testutil.Validator2, Weight: 200},
	}
	weights := resolveTrustWeights(participants)
	require.Equal(t, int64(100), weights[testutil.Validator])
	require.Equal(t, int64(200), weights[testutil.Validator2])
}

func TestCapComputeResultsToPreviousConfirmedWeight_ClampsAndDrops(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	const epoch = uint64(9)

	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epoch,
		EpochGroupId: epoch,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Validator, Weight: 100, CapWeight: 100},  // uncapped
			{Index: testutil.Validator2, Weight: 500, CapWeight: 100}, // clamped
			{Index: testutil.Executor, Weight: 300, CapWeight: 0},     // new -> dropped
		},
	}))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	eg := &epochgroup.EpochGroup{GroupData: &types.EpochGroupData{EpochIndex: epoch}}

	results := []stakingkeeper.ComputeResult{
		{Power: 100, OperatorAddress: valOperOf(t, testutil.Validator)},
		{Power: 500, OperatorAddress: valOperOf(t, testutil.Validator2)},
		{Power: 300, OperatorAddress: valOperOf(t, testutil.Executor)},
	}

	capped := am.capComputeResultsToPreviousConfirmedWeight(ctx, eg, results)

	powerByOp := map[string]int64{}
	for _, r := range capped {
		powerByOp[r.OperatorAddress] = r.Power
	}
	require.Len(t, capped, 2, "new participant dropped from validator set")
	require.Equal(t, int64(100), powerByOp[valOperOf(t, testutil.Validator)], "uncapped participant unchanged")
	require.Equal(t, int64(100), powerByOp[valOperOf(t, testutil.Validator2)], "over-weight participant clamped")
	_, ok := powerByOp[valOperOf(t, testutil.Executor)]
	require.False(t, ok, "new participant has no governance power")
}

func TestCapComputeResultsToPreviousConfirmedWeight_SkipsGuardians(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	const epoch = uint64(9)
	guardianOperator := valOperOf(t, testutil.Validator2)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.GenesisGuardianParams = &types.GenesisGuardianParams{
		GuardianAddresses: []string{guardianOperator},
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epoch,
		EpochGroupId: epoch,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Validator, Weight: 500, CapWeight: 100},
			{Index: testutil.Validator2, Weight: 500, CapWeight: 100},
		},
	}))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	eg := &epochgroup.EpochGroup{GroupData: &types.EpochGroupData{EpochIndex: epoch}}

	capped := am.capComputeResultsToPreviousConfirmedWeight(ctx, eg, []stakingkeeper.ComputeResult{
		{Power: 500, OperatorAddress: valOperOf(t, testutil.Validator)},
		{Power: 900, OperatorAddress: guardianOperator},
	})

	powerByOp := map[string]int64{}
	for _, r := range capped {
		powerByOp[r.OperatorAddress] = r.Power
	}
	require.Equal(t, int64(100), powerByOp[valOperOf(t, testutil.Validator)], "regular participant is capped")
	require.Equal(t, int64(900), powerByOp[guardianOperator], "guardian enhanced power is preserved")
}

func TestCapComputeResultsToPreviousConfirmedWeight_FallsBackWhenCapUnset(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	const epoch = uint64(11)

	// Pre-upgrade epoch: CapWeight not populated -> do not cap.
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epoch,
		EpochGroupId: epoch,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Validator, Weight: 100},
			{Index: testutil.Validator2, Weight: 500},
		},
	}))

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	eg := &epochgroup.EpochGroup{GroupData: &types.EpochGroupData{EpochIndex: epoch}}

	results := []stakingkeeper.ComputeResult{
		{Power: 100, OperatorAddress: valOperOf(t, testutil.Validator)},
		{Power: 500, OperatorAddress: valOperOf(t, testutil.Validator2)},
	}
	capped := am.capComputeResultsToPreviousConfirmedWeight(ctx, eg, results)
	require.Equal(t, results, capped, "no capping when CapWeight is unset")
}

func TestGetEffectiveValidationBaseState_UsesTrustWeightsForTotal(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	const epoch = uint64(9)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epoch))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epoch,
		EpochGroupId: epoch,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Validator, Weight: 100, CapWeight: 80},
			{Index: testutil.Validator2, Weight: 200, CapWeight: 120},
		},
	}))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   epoch,
		ModelId:      "",
		EpochGroupId: 77,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Validator, Weight: 100},
			{MemberAddress: testutil.Validator2, Weight: 200},
		},
	})

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	base := am.getEffectiveValidationBaseState(ctx)

	require.Equal(t, int64(200), base.totalWeight)
	require.Equal(t, int64(80), base.weights[testutil.Validator])
	require.Equal(t, int64(120), base.weights[testutil.Validator2])
}

func TestApplyPreviousConfirmedWeightCap_UntrustedModelDoesNotInflateCap(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	const prevEpoch = uint64(5)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, prevEpoch))

	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:                      prevEpoch,
		ModelId:                         "",
		SubGroupModels:                  []string{"model-a", "model-b"},
		ConfirmationAccountingSeparated: true,
		ConfirmationWeightScales: []*types.ConfirmationWeightScale{
			{ModelId: "model-a", WeightScaleFactor: types.DecimalFromFloat(1), HasTrustedVotingPower: true},
			{ModelId: "model-b", WeightScaleFactor: types.DecimalFromFloat(1), HasTrustedVotingPower: false},
		},
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Validator, Weight: 100, ConfirmationWeight: 10},
		},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: prevEpoch,
		ModelId:    "model-a",
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Validator, MlNodes: []*types.MLNodeInfo{{PocWeight: 10}}},
		},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: prevEpoch,
		ModelId:    "model-b",
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Validator, MlNodes: []*types.MLNodeInfo{{PocWeight: 90}}},
		},
	})

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	result := am.applyPreviousConfirmedWeightCap(ctx, []*types.ActiveParticipant{
		{Index: testutil.Validator, Weight: 100},
	})
	require.Equal(t, int64(10), result[0].CapWeight, "trusted-only confirmation must not be applied as 100% of root weight")
	require.Equal(t, int64(100), result[0].Weight)
}

func TestApplyPreviousConfirmedWeightCap_UntrustedOnlyModelGetsZeroCap(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)

	const prevEpoch = uint64(5)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, prevEpoch))

	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:                      prevEpoch,
		ModelId:                         "",
		SubGroupModels:                  []string{"model-b"},
		ConfirmationAccountingSeparated: true,
		ConfirmationWeightScales: []*types.ConfirmationWeightScale{
			{ModelId: "model-b", WeightScaleFactor: types.DecimalFromFloat(1), HasTrustedVotingPower: false},
		},
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Validator, Weight: 100, ConfirmationWeight: 0},
		},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: prevEpoch,
		ModelId:    "model-b",
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Validator, MlNodes: []*types.MLNodeInfo{{PocWeight: 100}}},
		},
	})

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	result := am.applyPreviousConfirmedWeightCap(ctx, []*types.ActiveParticipant{
		{Index: testutil.Validator, Weight: 100},
	})
	require.Equal(t, int64(0), result[0].CapWeight)
	require.Equal(t, int64(100), result[0].Weight)
}

func TestGetEffectiveValidationBaseState_IncludesEmptyVoterAccountingModels(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	const epoch = uint64(9)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epoch))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epoch,
		EpochGroupId: epoch,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Validator, Weight: 100, CapWeight: 80},
		},
	}))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:                      epoch,
		ModelId:                         "",
		EpochGroupId:                    77,
		SubGroupModels:                  []string{"model-a"},
		ConfirmationAccountingSeparated: true,
		ConfirmationWeightScales: []*types.ConfirmationWeightScale{
			{ModelId: "model-a", WeightScaleFactor: types.DecimalFromFloat(1), HasTrustedVotingPower: true},
			{ModelId: "model-b", WeightScaleFactor: types.DecimalFromFloat(1), HasTrustedVotingPower: false},
		},
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Validator, Weight: 100},
		},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   epoch,
		ModelId:      "model-a",
		EpochGroupId: 78,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Validator, Weight: 100, VotingPower: 80},
		},
	})

	am := NewAppModule(nil, k, nil, nil, nil, nil)
	base := am.getEffectiveValidationBaseState(ctx)

	byModel := map[string]bool{}
	for _, mvp := range base.existingModelVotingPowers {
		byModel[mvp.ModelId] = true
		if mvp.ModelId == "model-b" {
			require.Empty(t, mvp.VotingPowers, "untrusted model must appear as an explicit empty-voter model")
		}
	}
	require.True(t, byModel["model-a"])
	require.True(t, byModel["model-b"])
}
