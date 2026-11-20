package inference

import (
	"testing"

	"github.com/productscience/inference/testutil"
	coefficient "github.com/productscience/inference/x/inference/coefficients"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestEvaluateConfirmation_MeasuredWeightLimitedToEpochModels(t *testing.T) {
	const (
		epoch         = uint64(2)
		triggerHeight = int64(180)
		rootGroupID   = uint64(77)
		modelAGroupID = uint64(78)
		modelBGroupID = uint64(79)
		hostWeight    = int64(100)
	)

	tests := []struct {
		name             string
		commitModels     []string
		wantConfirmation int64
		wantStatus       types.ParticipantStatus
	}{
		{
			name:             "no commits",
			wantConfirmation: 0,
			wantStatus:       types.ParticipantStatus_INACTIVE,
		},
		{
			name:             "epoch model commit",
			commitModels:     []string{"model-a"},
			wantConfirmation: hostWeight,
			wantStatus:       types.ParticipantStatus_ACTIVE,
		},
		{
			name:             "model outside epoch",
			commitModels:     []string{"model-b"},
			wantConfirmation: 0,
			wantStatus:       types.ParticipantStatus_INACTIVE,
		},
		{
			name:             "epoch model and model outside epoch",
			commitModels:     []string{"model-a", "model-b"},
			wantConfirmation: hostWeight,
			wantStatus:       types.ParticipantStatus_ACTIVE,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, ctx, _ := newMinimalInferenceKeeperWithStub(t)

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
				Weight:       hostWeight,
				CapWeight:    hostWeight,
				Models:       []string{"model-a"},
				VotingPowers: []*types.ModelVotingPower{{ModelId: "model-a", VotingPower: 10}},
				MlNodes: []*types.ModelMLNodes{
					{MlNodes: []*types.MLNodeInfo{{NodeId: "node-1", PocWeight: hostWeight}}},
				},
			}
			validator := &types.ActiveParticipant{
				Index:     testutil.Validator,
				Weight:    400,
				CapWeight: 400,
				Models:    []string{"model-a", "model-b"},
				VotingPowers: []*types.ModelVotingPower{
					{ModelId: "model-a", VotingPower: 400},
					{ModelId: "model-b", VotingPower: 400},
				},
				MlNodes: []*types.ModelMLNodes{
					{MlNodes: []*types.MLNodeInfo{{NodeId: "v-node-a", PocWeight: 200}}},
					{MlNodes: []*types.MLNodeInfo{{NodeId: "v-node-b", PocWeight: 200}}},
				},
			}
			participants := []*types.ActiveParticipant{host, validator}

			scales := buildConfirmationWeightScales(
				[]string{"model-a", "model-b"},
				participants,
				&coefficient.Result{Scales: []*types.ConfirmationWeightScale{
					{ModelId: "model-a", EffectiveCoefficient: types.DecimalFromFloat(1)},
					{ModelId: "model-b", EffectiveCoefficient: types.DecimalFromFloat(1)},
				}},
			)
			require.Len(t, scales, 2)
			for _, scale := range scales {
				require.False(t, scale.ExcludeFromConfirmation)
			}

			k.SetEpochGroupData(ctx, types.EpochGroupData{
				EpochIndex:               epoch,
				EpochGroupId:             rootGroupID,
				SubGroupModels:           []string{"model-a", "model-b"},
				ConfirmationWeightScales: scales,
				ValidationWeights: []*types.ValidationWeight{
					{MemberAddress: host.Index, Weight: hostWeight, ConfirmationWeight: hostWeight},
					{MemberAddress: validator.Index, Weight: 400, ConfirmationWeight: 400},
				},
			})
			k.SetEpochGroupData(ctx, types.EpochGroupData{
				EpochIndex:   epoch,
				EpochGroupId: modelAGroupID,
				ModelId:      "model-a",
				ValidationWeights: []*types.ValidationWeight{
					{MemberAddress: host.Index, Weight: hostWeight, VotingPower: 10},
					{MemberAddress: validator.Index, Weight: 400, VotingPower: 400},
				},
			})
			k.SetEpochGroupData(ctx, types.EpochGroupData{
				EpochIndex:   epoch,
				EpochGroupId: modelBGroupID,
				ModelId:      "model-b",
				ValidationWeights: []*types.ValidationWeight{
					{MemberAddress: validator.Index, Weight: 400, VotingPower: 400},
				},
			})
			require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
				EpochId:          epoch,
				CapWeightApplied: true,
				Participants:     participants,
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

			am := NewAppModule(nil, k, nil, nil, nil, nil)
			am.captureConfirmationValidationSnapshot(ctx, triggerHeight, triggerHeight)
			_, found, err := k.GetPoCValidationSnapshot(ctx, triggerHeight)
			require.NoError(t, err)
			require.True(t, found)

			for _, model := range tt.commitModels {
				require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
					ParticipantAddress:       testutil.Executor,
					PocStageStartBlockHeight: triggerHeight,
					Count:                    uint32(hostWeight),
					RootHash:                 make([]byte, 32),
					CommitBlockHeight:        triggerHeight,
					ModelId:                  model,
				}))
				require.NoError(t, k.SetMLNodeWeightDistribution(ctx, types.MLNodeWeightDistribution{
					ParticipantAddress:       testutil.Executor,
					PocStageStartBlockHeight: triggerHeight,
					ModelId:                  model,
					Weights:                  []*types.MLNodeWeight{{NodeId: "node-" + model, Weight: uint32(hostWeight)}},
				}))
				require.NoError(t, k.SetPocValidationV2(ctx, types.PoCValidationV2{
					ParticipantAddress:          testutil.Executor,
					ValidatorParticipantAddress: testutil.Validator,
					PocStageStartBlockHeight:    triggerHeight,
					ValidatedWeight:             hostWeight,
					ModelId:                     model,
				}))
			}

			require.NoError(t, am.updateConfirmationWeights(ctx, &types.ConfirmationPoCEvent{
				EpochIndex:    epoch,
				EventSequence: 0,
				TriggerHeight: triggerHeight,
				Phase:         types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
			}))

			groupData, found := k.GetEpochGroupData(ctx, epoch, "")
			require.True(t, found)
			require.Equal(t, tt.wantConfirmation, groupData.ValidationWeights[0].ConfirmationWeight)

			participant, found := k.GetParticipant(ctx, testutil.Executor)
			require.True(t, found)
			require.Equal(t, tt.wantStatus, participant.Status)
		})
	}
}

func TestRestrictToEpochModels(t *testing.T) {
	nodesA := &types.ModelMLNodes{MlNodes: []*types.MLNodeInfo{{NodeId: "a", PocWeight: 10}}}
	nodesB := &types.ModelMLNodes{MlNodes: []*types.MLNodeInfo{{NodeId: "b", PocWeight: 20}}}
	epoch := []*types.ActiveParticipant{
		nil,
		{Index: "p1", Models: []string{"model-a", ""}},
		{Index: "p2", Models: []string{"model-a", "model-b"}},
	}
	measured := []*types.ActiveParticipant{
		nil,
		{Index: "p1", Models: []string{"model-a", "model-b"}, MlNodes: []*types.ModelMLNodes{nodesA, nodesB}},
		{Index: "p2", Models: []string{"model-b", "model-a"}, MlNodes: []*types.ModelMLNodes{nodesB}},
		{Index: "p3", Models: []string{"model-a"}, MlNodes: []*types.ModelMLNodes{nodesA}},
		{Index: "p1", Models: []string{""}, MlNodes: []*types.ModelMLNodes{nodesA}},
	}

	out := restrictToEpochModels(measured, epoch)

	require.Len(t, out, 4)
	require.Equal(t, "p1", out[0].Index)
	require.Equal(t, []string{"model-a"}, out[0].Models)
	require.Equal(t, []*types.ModelMLNodes{nodesA}, out[0].MlNodes)
	require.Equal(t, "p2", out[1].Index)
	require.Equal(t, []string{"model-b"}, out[1].Models)
	require.Equal(t, []*types.ModelMLNodes{nodesB}, out[1].MlNodes)
	require.Equal(t, "p3", out[2].Index)
	require.Empty(t, out[2].Models)
	require.Empty(t, out[2].MlNodes)
	require.Empty(t, out[3].Models)

	require.Equal(t, []string{"model-a", "model-b"}, measured[1].Models)
	require.Len(t, measured[1].MlNodes, 2)
}

func TestDecideCurrentChallengeSegment_ModelOutsideEpochDoesNotCount(t *testing.T) {
	for _, tc := range []struct {
		name          string
		inEpoch       bool
		wantState     types.PoCChallengeState
		wantConfirmed int64
	}{
		{
			name:          "work on an epoch model counts",
			inEpoch:       true,
			wantState:     types.PoCChallengeState_POC_CHALLENGE_STATE_PASSED,
			wantConfirmed: 88,
		},
		{
			name:          "work on a model outside the epoch does not count",
			inEpoch:       false,
			wantState:     types.PoCChallengeState_POC_CHALLENGE_STATE_CHALLENGE_FAILED,
			wantConfirmed: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			am, k, ctx := prepareChallengeEval(t, 400, 0, []challengeEvalModel{
				{id: "assigned", weight: 100},
				{id: "substitute", weight: 0, count: 88, accept: true},
			})
			if !tc.inEpoch {
				active, found := k.GetActiveParticipants(ctx, 2)
				require.True(t, found)
				active.Participants[0].Models = active.Participants[0].Models[:1]
				active.Participants[0].MlNodes = active.Participants[0].MlNodes[:1]
				require.NoError(t, k.SetActiveParticipants(ctx, active))
			}

			require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 500, 180, false))

			ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, tc.wantState, ch.State)
			group, found := k.GetEpochGroupData(ctx, 2, "")
			require.True(t, found)
			require.Equal(t, tc.wantConfirmed, group.ValidationWeights[0].ConfirmationWeight)
		})
	}
}
