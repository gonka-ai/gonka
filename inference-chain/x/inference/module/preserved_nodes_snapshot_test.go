package inference

import (
	"testing"

	mathsdk "cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
)

func TestCaptureGenerationStartTimestampStoresSnapshots(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	snapshot := types.PreservedNodesSnapshot{
		EpisodeAnchorHeight: 300,
		ModelPreservedNodes: []*types.ModelPreservedNodes{
			{
				ModelId: "model-a",
				Participants: []*types.ParticipantPreservedNodes{
					{ParticipantId: testutil.Executor, NodeIds: []string{"node-1"}},
				},
			},
		},
	}

	err := am.captureGenerationStartTimestamp(ctx, 1234, 300, snapshot)
	require.NoError(t, err)

	validationSnapshot, found, err := k.GetPoCValidationSnapshot(ctx, 300)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(1234), validationSnapshot.GenerationStartTimestamp)

	preservedSnapshot, found, err := k.GetPreservedNodesSnapshot(ctx)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, snapshot, preservedSnapshot)
}

func TestPartitionWeightByPreservation(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 5,
		Participants: []*types.ActiveParticipant{
			{
				Index:  testutil.Executor,
				Models: []string{"model-a"},
				MlNodes: []*types.ModelMLNodes{
					{
						MlNodes: []*types.MLNodeInfo{
							{NodeId: "node-1", PocWeight: 10},
							{NodeId: "node-2", PocWeight: 20},
						},
					},
				},
			},
		},
	}))

	preserved, notPreserved, err := am.partitionWeightByPreservation(
		ctx,
		5,
		map[string]mathsdk.LegacyDec{"model-a": mathsdk.LegacyOneDec()},
		&types.PreservedNodesSnapshot{
			EpisodeAnchorHeight: 321,
			ModelPreservedNodes: []*types.ModelPreservedNodes{
				{
					ModelId: "model-a",
					Participants: []*types.ParticipantPreservedNodes{
						{ParticipantId: testutil.Executor, NodeIds: []string{"node-1"}},
					},
				},
			},
		},
	)
	require.NoError(t, err)
	require.Equal(t, int64(10), preserved[testutil.Executor])
	require.Equal(t, int64(20), notPreserved[testutil.Executor])
}

func TestGetInferenceServingNodeIdsUsesUpcomingEpochAnchor(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	require.NoError(t, k.SetPreservedNodesSnapshot(ctx, types.PreservedNodesSnapshot{
		EpisodeAnchorHeight: 100,
		ModelPreservedNodes: []*types.ModelPreservedNodes{
			{
				ModelId: "model-a",
				Participants: []*types.ParticipantPreservedNodes{
					{ParticipantId: testutil.Executor, NodeIds: []string{"node-1"}},
				},
			},
		},
	}))

	inferenceServingNodeIds := am.getInferenceServingNodeIds(ctx, types.Epoch{Index: 2, PocStartBlockHeight: 100})
	require.Contains(t, inferenceServingNodeIds, testutil.Executor)
	require.Contains(t, inferenceServingNodeIds[testutil.Executor], "node-1")
}

func TestComputeNewWeightsCarriesPreservedNodesFromRegularSnapshot(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	currentEpoch := types.Epoch{Index: 1, PocStartBlockHeight: 50}
	require.NoError(t, k.SetEpoch(ctx, &currentEpoch))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpoch.Index))

	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:          currentEpoch.Index,
		PocStartBlockHeight: uint64(currentEpoch.PocStartBlockHeight),
		ValidationWeights: []*types.ValidationWeight{
			{
				MemberAddress: testutil.Executor,
				Weight:        30,
			},
		},
		SubGroupModels: []string{"model-a"},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: currentEpoch.Index,
		ModelId:    "model-a",
		ValidationWeights: []*types.ValidationWeight{
			{
				MemberAddress: testutil.Executor,
				Weight:        30,
				MlNodes: []*types.MLNodeInfo{
					{NodeId: "node-1", PocWeight: 10, TimeslotAllocation: []bool{true, false}},
					{NodeId: "node-2", PocWeight: 20, TimeslotAllocation: []bool{true, false}},
				},
			},
		},
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
		EpisodeAnchorHeight: 100,
		ModelPreservedNodes: []*types.ModelPreservedNodes{
			{
				ModelId: "model-a",
				Participants: []*types.ParticipantPreservedNodes{
					{ParticipantId: testutil.Executor, NodeIds: []string{"node-1"}},
				},
			},
		},
	}))

	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 100,
		Count:                    10,
		RootHash:                 make([]byte, 32),
		CommitBlockHeight:        100,
		ModelId:                  "model-a",
	}))
	require.NoError(t, k.SetMLNodeWeightDistribution(ctx, types.MLNodeWeightDistribution{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 100,
		ModelId:                  "model-a",
		Weights: []*types.MLNodeWeight{
			{NodeId: "node-1", Weight: 10},
		},
	}))

	result := am.ComputeNewWeights(ctx, types.Epoch{Index: 2, PocStartBlockHeight: 100})
	require.Len(t, result, 1)
	require.Equal(t, testutil.Executor, result[0].Index)
	require.Equal(t, int64(10), result[0].Weight)
	require.Equal(t, []string{"model-a"}, result[0].Models)
	require.Len(t, result[0].MlNodes, 1)
	require.Len(t, result[0].MlNodes[0].MlNodes, 1)
	require.Equal(t, "node-1", result[0].MlNodes[0].MlNodes[0].NodeId)
}
