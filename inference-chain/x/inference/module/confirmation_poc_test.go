package inference

import (
	"testing"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestEvaluateConfirmation_InsufficientModelVotingPower(t *testing.T) {
	const (
		epoch         = uint64(2)
		triggerHeight = int64(180)
		rootGroupID   = uint64(77)
		modelAGroupID = uint64(78)
		modelBGroupID = uint64(79)
	)

	tests := []struct {
		name                 string
		modelBHadVotingPower bool
	}{
		{
			name:                 "formation voter later loses quorum",
			modelBHadVotingPower: true,
		},
		{
			name:                 "model without formation voting power follows the same accounting",
			modelBHadVotingPower: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, ctx, groupStub := newMinimalInferenceKeeperWithStub(t)

			params, err := k.GetParams(ctx)
			require.NoError(t, err)
			params.PocParams = &types.PocParams{
				Models: []*types.PoCModelConfig{
					{ModelId: "model-a", WeightScaleFactor: types.DecimalFromFloat(1)},
					{ModelId: "model-b", WeightScaleFactor: types.DecimalFromFloat(1)},
				},
			}
			params.ConfirmationPocParams.AlphaThreshold = types.DecimalFromFloat(0.5)
			require.NoError(t, k.SetParams(ctx, params))
			require.NoError(t, k.PrecomputeSPRTValues(ctx))
			require.NoError(t, k.SetEffectiveEpochIndex(ctx, epoch))

			host := &types.ActiveParticipant{
				Index:        testutil.Executor,
				Weight:       100,
				CapWeight:    40,
				Models:       []string{"model-a", "model-b"},
				VotingPowers: []*types.ModelVotingPower{{ModelId: "model-a", VotingPower: 40}},
				MlNodes: []*types.ModelMLNodes{
					{MlNodes: []*types.MLNodeInfo{{NodeId: "node-a", PocWeight: 40}}},
					{MlNodes: []*types.MLNodeInfo{{NodeId: "node-b", PocWeight: 60}}},
				},
			}
			modelAValidator := &types.ActiveParticipant{
				Index:        testutil.Validator,
				Weight:       60,
				CapWeight:    60,
				VotingPowers: []*types.ModelVotingPower{{ModelId: "model-a", VotingPower: 60}},
			}
			formationParticipants := []*types.ActiveParticipant{host, modelAValidator}
			rootWeights := []*types.ValidationWeight{
				{MemberAddress: host.Index, Weight: host.Weight},
				{MemberAddress: modelAValidator.Index, Weight: modelAValidator.Weight},
			}
			subGroupModels := []string{"model-a"}
			if tt.modelBHadVotingPower {
				// Before removal: total voting weight is 190 and model B has
				// 130, so B can exceed the default 2/3 validation threshold.
				host.VotingPowers = append(host.VotingPowers,
					&types.ModelVotingPower{ModelId: "model-b", VotingPower: 40})
				removedModelBValidator := &types.ActiveParticipant{
					Index:        testutil.Requester,
					Weight:       90,
					CapWeight:    90,
					VotingPowers: []*types.ModelVotingPower{{ModelId: "model-b", VotingPower: 90}},
				}
				formationParticipants = append(formationParticipants, removedModelBValidator)
				rootWeights = append(rootWeights,
					&types.ValidationWeight{MemberAddress: removedModelBValidator.Index, Weight: removedModelBValidator.Weight})
				subGroupModels = append(subGroupModels, "model-b")
			}

			scales := buildConfirmationWeightScales(
				[]string{"model-a", "model-b"},
				formationParticipants,
				params.PocParams,
			)
			require.Len(t, scales, 2)
			initialConfirmationWeight := types.ConfirmationWeightOfParticipantWithCoefficients(
				host,
				types.ConfirmationWeightCoefficients(scales),
			)
			require.Equal(t, int64(100), initialConfirmationWeight)

			k.SetEpochGroupData(ctx, types.EpochGroupData{
				EpochIndex:               epoch,
				EpochGroupId:             rootGroupID,
				SubGroupModels:           subGroupModels,
				ConfirmationWeightScales: scales,
				ValidationWeights:        rootWeights,
			})
			rootData, found := k.GetEpochGroupData(ctx, epoch, "")
			require.True(t, found)
			rootData.ValidationWeights[0].ConfirmationWeight = initialConfirmationWeight
			k.SetEpochGroupData(ctx, rootData)
			k.SetEpochGroupData(ctx, types.EpochGroupData{
				EpochIndex:   epoch,
				EpochGroupId: modelAGroupID,
				ModelId:      "model-a",
				ValidationWeights: []*types.ValidationWeight{
					{MemberAddress: host.Index, Weight: host.Weight, VotingPower: 40},
					{MemberAddress: modelAValidator.Index, Weight: modelAValidator.Weight, VotingPower: 60},
				},
			})
			if tt.modelBHadVotingPower {
				k.SetEpochGroupData(ctx, types.EpochGroupData{
					EpochIndex:   epoch,
					EpochGroupId: modelBGroupID,
					ModelId:      "model-b",
					ValidationWeights: []*types.ValidationWeight{
						{MemberAddress: host.Index, Weight: host.Weight, VotingPower: 40},
						{MemberAddress: testutil.Requester, Weight: 90, VotingPower: 90},
					},
				})
			}
			require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
				EpochId:          epoch,
				CapWeightApplied: true,
				Participants:     formationParticipants,
			}))

			require.NoError(t, k.SetParticipant(ctx, types.Participant{
				Index:             testutil.Executor,
				Address:           testutil.Executor,
				ValidatorKey:      "validator-key",
				InferenceUrl:      "http://executor.example.com",
				Status:            types.ParticipantStatus_ACTIVE,
				CurrentEpochStats: types.NewCurrentEpochStats(),
			}))
			k.SetRandomSeed(ctx, types.RandomSeed{
				Participant: testutil.Executor,
				EpochIndex:  epoch,
				Signature:   "seed-signature",
			})

			if tt.modelBHadVotingPower {
				currentGroup, err := k.GetCurrentEpochGroup(ctx)
				require.NoError(t, err)
				require.NoError(t, currentGroup.RemoveMember(ctx, &types.Participant{
					Index:   testutil.Requester,
					Address: testutil.Requester,
				}))
				require.True(t, groupStub.excludedMembersByGroup[rootGroupID][testutil.Requester])
				require.True(t, groupStub.excludedMembersByGroup[modelBGroupID][testutil.Requester])
			}

			am := NewAppModule(nil, k, nil, nil, nil, nil)
			am.captureConfirmationValidationSnapshot(ctx, triggerHeight, triggerHeight)
			snapshot, found, err := k.GetPoCValidationSnapshot(ctx, triggerHeight)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, int64(100), snapshot.TotalNetworkWeight)
			var modelBVotingPower int64
			for _, modelVotingPowers := range snapshot.ModelVotingPowers {
				if modelVotingPowers.ModelId != "model-b" {
					continue
				}
				for _, votingPower := range modelVotingPowers.VotingPowers {
					modelBVotingPower += votingPower.VotingPower
				}
			}
			if tt.modelBHadVotingPower {
				require.Equal(t, int64(40), modelBVotingPower)
			} else {
				require.Zero(t, modelBVotingPower)
			}

			for _, model := range []struct {
				id     string
				nodeID string
				weight int64
			}{
				{id: "model-a", nodeID: "node-a", weight: 40},
				{id: "model-b", nodeID: "node-b", weight: 60},
			} {
				require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
					ParticipantAddress:       testutil.Executor,
					PocStageStartBlockHeight: triggerHeight,
					Count:                    uint32(model.weight),
					RootHash:                 make([]byte, 32),
					CommitBlockHeight:        triggerHeight,
					ModelId:                  model.id,
				}))
				require.NoError(t, k.SetMLNodeWeightDistribution(ctx, types.MLNodeWeightDistribution{
					ParticipantAddress:       testutil.Executor,
					PocStageStartBlockHeight: triggerHeight,
					ModelId:                  model.id,
					Weights: []*types.MLNodeWeight{{
						NodeId: model.nodeID,
						Weight: uint32(model.weight),
					}},
				}))
				require.NoError(t, k.SetPocValidationV2(ctx, types.PoCValidationV2{
					ParticipantAddress:          testutil.Executor,
					ValidatorParticipantAddress: testutil.Validator,
					PocStageStartBlockHeight:    triggerHeight,
					ValidatedWeight:             model.weight,
					ModelId:                     model.id,
				}))
			}

			require.NoError(t, k.SetPocValidationV2(ctx, types.PoCValidationV2{
				ParticipantAddress:          testutil.Executor,
				ValidatorParticipantAddress: testutil.Executor,
				PocStageStartBlockHeight:    triggerHeight,
				ValidatedWeight:             40,
				ModelId:                     "model-a",
			}))

			require.NoError(t, am.updateConfirmationWeights(ctx, &types.ConfirmationPoCEvent{
				EpochIndex:    epoch,
				EventSequence: 0,
				TriggerHeight: triggerHeight,
				Phase:         types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
			}))

			groupData, found := k.GetEpochGroupData(ctx, epoch, "")
			require.True(t, found)
			require.Equal(t, int64(40), groupData.ValidationWeights[0].ConfirmationWeight)

			participant, found := k.GetParticipant(ctx, testutil.Executor)
			require.True(t, found)
			require.Equal(t, types.ParticipantStatus_INACTIVE, participant.Status)
			require.NotNil(t, participant.CurrentEpochStats.ConfirmationPoCRatio)
			expectedRatio := decimal.NewFromInt(40).Div(decimal.NewFromInt(100)).Div(pocDeviationCoeff)
			require.True(
				t,
				participant.CurrentEpochStats.ConfirmationPoCRatio.ToDecimal().Equal(expectedRatio),
				"got ratio %s, expected %s",
				participant.CurrentEpochStats.ConfirmationPoCRatio.String(),
				expectedRatio.String(),
			)
		})
	}
}

// Review-3 verification scenario: 1 participant with 2 MLNodes (weights 10 and 1).
// Rotating preserved across confirmation events. Honest at every event -> reward
// stays at the full-weight reading of 11. A dishonest event collapses it exactly.
func TestFoldEventReadings_RotatingPreservedHonestThenDishonest(t *testing.T) {
	addr := "participant1"
	initial := &types.EpochGroupData{
		EpochIndex: 1,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: addr, Weight: 11, ConfirmationWeight: 11},
		},
	}

	// Event 1: preserved = {node-A(10)}. Participating = {node-B(1)}. Honest -> measured 1.
	// reading = 10 + 1 = 11 = initial; no change.
	updated, ratios := foldEventReadings(
		initial,
		map[string]int64{addr: 1},  // measured from node-B
		map[string]int64{addr: 10}, // preservedHere
		map[string]int64{addr: 11}, // formation-time expected
		nil,
	)
	require.False(t, updated, "honest reading equal to initial must not lower ConfirmationWeight")
	require.Equal(t, int64(11), initial.ValidationWeights[0].ConfirmationWeight)
	requireRatioEqual(t, ratios[addr], 1, 1)

	// Event 2: preserved rotated -> {node-B(1)}. Participating = {node-A(10)}. Honest -> measured 10.
	// reading = 1 + 10 = 11; still no change.
	updated, ratios = foldEventReadings(
		initial,
		map[string]int64{addr: 10},
		map[string]int64{addr: 1},
		map[string]int64{addr: 11},
		nil,
	)
	require.False(t, updated, "honest reading with rotated preservation must also stay at 11")
	require.Equal(t, int64(11), initial.ValidationWeights[0].ConfirmationWeight)
	requireRatioEqual(t, ratios[addr], 1, 1)

	// Event 3: preserved = {node-B(1)}. Participating node-A(10) cheats -> measured 4.
	// reading = 1 + 4 = 5; ConfirmationWeight drops to 5.
	updated, ratios = foldEventReadings(
		initial,
		map[string]int64{addr: 4},
		map[string]int64{addr: 1},
		map[string]int64{addr: 11},
		nil,
	)
	require.True(t, updated, "dishonest event must lower ConfirmationWeight")
	require.Equal(t, int64(5), initial.ValidationWeights[0].ConfirmationWeight)
	// Slashing ratio at this event: reading/totalExpected = 5/11 ~= 0.45;
	// divided by pocDeviationCoeff(0.909) ~= 0.50.
	slashRatio := ratios[addr].ToDecimal()
	require.True(t, slashRatio.LessThan(decimal.NewFromFloat(0.6)), "slashing must kick in on dishonest event")
	require.True(t, slashRatio.GreaterThan(decimal.NewFromFloat(0.4)), "slashing ratio matches reading/totalExpected with deviation coeff")
}

// Honest operation with all-preserved for a participant: measured = 0 but
// reading = preserved, which must not cause the participant to be slashed or
// lose their ConfirmationWeight.
func TestFoldEventReadings_AllPreservedZeroMeasuredIsNotPenalized(t *testing.T) {
	addr := "participant1"
	ege := &types.EpochGroupData{
		EpochIndex: 1,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: addr, Weight: 100, ConfirmationWeight: 100},
		},
	}

	updated, ratios := foldEventReadings(
		ege,
		map[string]int64{addr: 0},   // participant submitted nothing for this event
		map[string]int64{addr: 100}, // every one of their nodes was preserved this event
		map[string]int64{addr: 100},
		nil,
	)

	require.False(t, updated)
	require.Equal(t, int64(100), ege.ValidationWeights[0].ConfirmationWeight)
	requireRatioEqual(t, ratios[addr], 1, 1)
}

// No expected confirmation at all results in no ratio write and no change to
// ConfirmationWeight, because this event has no enforceable weight for the
// participant.
func TestFoldEventReadings_EmptyEventKeepsRatioAtOne(t *testing.T) {
	addr := "participant1"
	ege := &types.EpochGroupData{
		EpochIndex: 1,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: addr, Weight: 50, ConfirmationWeight: 50},
		},
	}

	updated, ratios := foldEventReadings(
		ege,
		map[string]int64{},
		map[string]int64{},
		map[string]int64{},
		nil,
	)

	require.False(t, updated)
	require.Equal(t, int64(50), ege.ValidationWeights[0].ConfirmationWeight)
	require.NotContains(t, ratios, addr)
}

// Maintenance-covered participants must keep prior ConfirmationWeight and get
// no ratio entry, even when measured weight is zero (offline during the window).
func TestFoldEventReadings_MaintenanceExemptSkipsWeightAndRatio(t *testing.T) {
	maint := "maintenance-host"
	other := "online-host"
	ege := &types.EpochGroupData{
		EpochIndex: 1,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: maint, Weight: 100, ConfirmationWeight: 100},
			{MemberAddress: other, Weight: 50, ConfirmationWeight: 50},
		},
	}

	updated, ratios := foldEventReadings(
		ege,
		map[string]int64{maint: 0, other: 10},
		map[string]int64{maint: 0, other: 0},
		map[string]int64{maint: 100, other: 50},
		map[string]struct{}{maint: {}},
	)

	require.True(t, updated, "online host with poor reading must still be folded")
	require.Equal(t, int64(100), ege.ValidationWeights[0].ConfirmationWeight, "maintenance host weight preserved")
	require.Equal(t, int64(10), ege.ValidationWeights[1].ConfirmationWeight, "online host weight lowered")
	require.NotContains(t, ratios, maint)
	require.Contains(t, ratios, other)
}

func TestConfirmationScalesInSnapshot(t *testing.T) {
	scales := []*types.ConfirmationWeightScale{
		{ModelId: "model-a", WeightScaleFactor: types.DecimalFromFloat(1)},
		{ModelId: "model-b", WeightScaleFactor: types.DecimalFromFloat(2)},
		{ModelId: "model-c", WeightScaleFactor: types.DecimalFromFloat(3)},
	}
	got := confirmationScalesInSnapshot(scales, []*types.ModelVotingPowers{
		{ModelId: "model-c"},
		{ModelId: "model-a"},
		{ModelId: "extra-model"},
	})

	require.Equal(t, []*types.ConfirmationWeightScale{scales[0], scales[2]}, got)
}

func requireRatioEqual(t *testing.T, got *types.Decimal, numerator, denominator int64) {
	t.Helper()
	require.NotNil(t, got)
	expected := decimal.NewFromInt(numerator).Div(decimal.NewFromInt(denominator))
	require.True(t, got.ToDecimal().Equal(expected), "got=%s expected=%s", got.ToDecimal().String(), expected.String())
}
