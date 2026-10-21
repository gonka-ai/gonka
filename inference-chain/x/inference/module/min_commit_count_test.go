package inference

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func setMinCountParams(t *testing.T, k keeper.Keeper, ctx sdk.Context, minCounts map[string]uint32) {
	t.Helper()
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	models := make([]*types.PoCModelConfig, 0, len(minCounts))
	for _, modelID := range []string{"model-a", "model-b"} {
		minCount, ok := minCounts[modelID]
		if !ok {
			continue
		}
		statTest := types.DefaultPoCStatTestParams()
		statTest.MinCount = minCount
		models = append(models, &types.PoCModelConfig{
			ModelId:           modelID,
			WeightScaleFactor: types.DecimalFromFloat(1.0),
			StatTest:          statTest,
		})
	}
	params.PocParams = &types.PocParams{
		ValidationSampleSize: 200,
		Models:               models,
	}
	require.NoError(t, k.SetParams(ctx, params))
}

func TestDropCommitsBelowMinCount(t *testing.T) {
	k, _ := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	statTest := types.DefaultPoCStatTestParams()
	statTest.MinCount = 200
	pocParams := &types.PocParams{Models: []*types.PoCModelConfig{{ModelId: "model-a", StatTest: statTest}}}

	key := func(addr, model string) types.PoCParticipantModelKey {
		return types.PoCParticipantModelKey{ParticipantAddress: addr, ModelID: model}
	}
	commits := map[types.PoCParticipantModelKey]types.PoCV2StoreCommit{
		key("a", "model-a"): {Count: 4},
		key("b", "model-a"): {Count: 199},
		key("c", "model-a"): {Count: 200},
		key("d", "model-a"): {Count: 201},
		key("e", "model-b"): {Count: 4},
	}

	kept := am.dropCommitsBelowMinCount(commits, pocParams)
	require.Len(t, kept, 3)
	require.Contains(t, kept, key("c", "model-a"))
	require.Contains(t, kept, key("d", "model-a"))
	require.Contains(t, kept, key("e", "model-b"))
	require.Len(t, commits, 5)

	require.Len(t, am.dropCommitsBelowMinCount(commits, &types.PocParams{}), 5)
	require.Len(t, am.dropCommitsBelowMinCount(commits, nil), 5)
}

func TestComputeNewWeightsMinCountUsesCommittedCount(t *testing.T) {
	const (
		stage       = int64(100)
		servingNode = "node-1"
		freshNode   = "node-2"
	)

	cases := []struct {
		name         string
		minCount     uint32
		distribution []*types.MLNodeWeight
		expectFresh  bool
	}{
		{
			name:     "small fresh share of a large commit keeps its weight",
			minCount: 200,
			distribution: []*types.MLNodeWeight{
				{NodeId: servingNode, Weight: 250},
				{NodeId: freshNode, Weight: 150},
			},
			expectFresh: true,
		},
		{
			name:         "small commit gets no weight",
			minCount:     200,
			distribution: []*types.MLNodeWeight{{NodeId: freshNode, Weight: 150}},
			expectFresh:  false,
		},
		{
			name:         "disabled threshold keeps small commit",
			minCount:     0,
			distribution: []*types.MLNodeWeight{{NodeId: freshNode, Weight: 150}},
			expectFresh:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx := newMinimalInferenceKeeper(t)
			am := NewAppModule(nil, k, nil, nil, nil, nil)
			setMinCountParams(t, k, ctx, map[string]uint32{"model-a": tc.minCount})

			currentEpoch := types.Epoch{Index: 1, PocStartBlockHeight: 50}
			require.NoError(t, k.SetEpoch(ctx, &currentEpoch))
			require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpoch.Index))
			k.SetEpochGroupData(ctx, types.EpochGroupData{
				EpochIndex:          currentEpoch.Index,
				PocStartBlockHeight: uint64(currentEpoch.PocStartBlockHeight),
				SubGroupModels:      []string{"model-a"},
			})

			require.NoError(t, k.SetParticipant(ctx, types.Participant{
				Index:        testutil.Executor,
				Address:      testutil.Executor,
				ValidatorKey: "validator-key",
				InferenceUrl: "http://executor",
				Status:       types.ParticipantStatus_ACTIVE,
			}))
			require.NoError(t, k.SetRandomSeed(ctx, types.RandomSeed{
				Participant: testutil.Executor,
				EpochIndex:  2,
				Signature:   "seed-signature",
			}))
			require.NoError(t, k.SetPreservedNodesSnapshot(ctx, types.PreservedNodesSnapshot{
				EpisodeAnchorHeight: stage,
				ModelPreservedNodes: []*types.ModelPreservedNodes{{
					ModelId: "model-a",
					Participants: []*types.ParticipantPreservedNodes{
						{ParticipantId: testutil.Executor, NodeIds: []string{servingNode}},
					},
				}},
			}))

			var count uint32
			for _, w := range tc.distribution {
				count += w.Weight
			}
			require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
				ParticipantAddress:       testutil.Executor,
				PocStageStartBlockHeight: stage,
				Count:                    count,
				RootHash:                 make([]byte, 32),
				CommitBlockHeight:        stage,
				ModelId:                  "model-a",
			}))
			require.NoError(t, k.SetMLNodeWeightDistribution(ctx, types.MLNodeWeightDistribution{
				ParticipantAddress:       testutil.Executor,
				PocStageStartBlockHeight: stage,
				ModelId:                  "model-a",
				Weights:                  tc.distribution,
			}))
			require.NoError(t, k.SetPocValidationV2(ctx, types.PoCValidationV2{
				ParticipantAddress:          testutil.Executor,
				ValidatorParticipantAddress: testutil.Validator,
				PocStageStartBlockHeight:    stage,
				ValidatedWeight:             int64(count),
				ModelId:                     "model-a",
			}))
			require.NoError(t, k.SetPoCValidationSnapshot(ctx, types.PoCValidationSnapshot{
				PocStageStartHeight: stage,
				SnapshotHeight:      stage,
				AppHash:             "test-hash",
				ModelVotingPowers: []*types.ModelVotingPowers{{
					ModelId:      "model-a",
					VotingPowers: []*types.VotingPowerEntry{{Address: testutil.Validator, VotingPower: 100}},
				}},
				TotalNetworkWeight: 100,
			}))

			computed := am.computeNewWeights(ctx, types.Epoch{Index: 2, PocStartBlockHeight: stage})

			_, fresh := computed.freshNodeIDs[testutil.Executor][freshNode]
			require.Equal(t, tc.expectFresh, fresh)
			if !tc.expectFresh {
				return
			}
			var weight int64
			for _, p := range computed.participants {
				if p.Index == testutil.Executor {
					weight = p.Weight
				}
			}
			require.Equal(t, int64(150), weight)
		})
	}
}

func TestUpdateConfirmationWeightsV2_DropsCommitsBelowMinCount(t *testing.T) {
	cases := []struct {
		name      string
		minCountA uint32
		expected  int64
	}{
		{"threshold disabled", 0, 20},
		{"commit at threshold", 10, 20},
		{"commit below threshold", 11, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")
			k, ctx := newMinimalInferenceKeeper(t)
			setMinCountParams(t, k, ctx, map[string]uint32{"model-a": tc.minCountA, "model-b": 0})
			am := NewAppModule(nil, k, nil, nil, nil, nil)

			require.NoError(t, k.SetParticipant(ctx, types.Participant{
				Index:        testutil.Executor,
				Address:      testutil.Executor,
				ValidatorKey: "validator-key",
				InferenceUrl: "http://example.com/",
			}))
			k.SetRandomSeed(ctx, types.RandomSeed{Participant: testutil.Executor, EpochIndex: 2, Signature: "sig"})

			votingPowers := make([]*types.ModelVotingPowers, 0, 2)
			for _, modelID := range []string{"model-a", "model-b"} {
				require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
					ParticipantAddress:       testutil.Executor,
					PocStageStartBlockHeight: 180,
					Count:                    10,
					RootHash:                 make([]byte, 32),
					CommitBlockHeight:        180,
					ModelId:                  modelID,
				}))
				require.NoError(t, k.SetMLNodeWeightDistribution(ctx, types.MLNodeWeightDistribution{
					ParticipantAddress:       testutil.Executor,
					PocStageStartBlockHeight: 180,
					ModelId:                  modelID,
					Weights:                  []*types.MLNodeWeight{{NodeId: "node-" + modelID, Weight: 10}},
				}))
				require.NoError(t, k.SetPocValidationV2(ctx, types.PoCValidationV2{
					ParticipantAddress:          testutil.Executor,
					ValidatorParticipantAddress: testutil.Validator,
					PocStageStartBlockHeight:    180,
					ValidatedWeight:             10,
					ModelId:                     modelID,
				}))
				votingPowers = append(votingPowers, &types.ModelVotingPowers{
					ModelId:      modelID,
					VotingPowers: []*types.VotingPowerEntry{{Address: testutil.Validator, VotingPower: 100}},
				})
			}

			event := &types.ConfirmationPoCEvent{
				EpochIndex:            2,
				TriggerHeight:         180,
				GenerationStartHeight: 190,
				Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
			}
			snapshot := types.PoCValidationSnapshot{
				PocStageStartHeight: 180,
				SnapshotHeight:      190,
				AppHash:             "test-hash",
				ModelVotingPowers:   votingPowers,
				TotalNetworkWeight:  100,
			}

			result := am.updateConfirmationWeightsV2(ctx, event, snapshot)
			require.Len(t, result, 1)
			require.Equal(t, tc.expected, result[0].Weight)
		})
	}
}

func TestDecideCurrentChallengeSegment_DropsCommitsBelowMinCount(t *testing.T) {
	cases := []struct {
		name     string
		minCount uint32
		state    types.PoCChallengeState
	}{
		{"threshold disabled", 0, types.PoCChallengeState_POC_CHALLENGE_STATE_PASSED},
		{"commit at threshold", 80, types.PoCChallengeState_POC_CHALLENGE_STATE_PASSED},
		{"commit below threshold", 81, types.PoCChallengeState_POC_CHALLENGE_STATE_CHALLENGE_FAILED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			am, k, ctx := prepareChallengeEval(t, 400, 0, []challengeEvalModel{
				{id: "m1", weight: 80, count: 80, accept: true},
			})
			params, err := k.GetParams(ctx)
			require.NoError(t, err)
			statTest := types.DefaultPoCStatTestParams()
			statTest.MinCount = tc.minCount
			params.PocParams.Models = []*types.PoCModelConfig{{
				ModelId:           "m1",
				WeightScaleFactor: types.DecimalFromFloat(1.0),
				StatTest:          statTest,
			}}
			require.NoError(t, k.SetParams(ctx, params))

			require.NoError(t, am.decideCurrentChallengeSegments(ctx, 2, 500, 180, false))
			ch, found, err := k.GetPoCChallenge(ctx, testutil.Executor)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, tc.state, ch.State)
		})
	}
}
