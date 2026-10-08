package inference

import (
	"testing"

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

func TestPreservedWeightByParticipantFiltersToConfirmationScales(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{
			Index:  testutil.Executor,
			Models: []string{"model-a", "model-b"},
			MlNodes: []*types.ModelMLNodes{
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "node-a-1", PocWeight: 10},
						{NodeId: "node-a-2", PocWeight: 20},
					},
				},
				{
					MlNodes: []*types.MLNodeInfo{
						{NodeId: "node-b-1", PocWeight: 100},
					},
				},
			},
		},
	}
	preserved := preservedWeightByParticipant(
		participants,
		&types.PreservedNodesSnapshot{
			ModelPreservedNodes: []*types.ModelPreservedNodes{
				{
					ModelId: "model-a",
					Participants: []*types.ParticipantPreservedNodes{
						{ParticipantId: testutil.Executor, NodeIds: []string{"node-a-1"}},
					},
				},
				{
					ModelId: "model-b",
					Participants: []*types.ParticipantPreservedNodes{
						{ParticipantId: testutil.Executor, NodeIds: []string{"node-b-1"}},
					},
				},
			},
		},
		[]*types.ConfirmationWeightScale{
			{ModelId: "model-a", WeightScaleFactor: types.DecimalFromFloat(2.0)},
		},
	)

	require.Equal(t, int64(20), preserved[testutil.Executor])
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

	computed := am.computeNewWeights(ctx, types.Epoch{Index: 2, PocStartBlockHeight: 100})
	result := computed.participants
	require.Len(t, result, 1)
	require.Equal(t, testutil.Executor, result[0].Index)
	require.Equal(t, int64(10), result[0].Weight)
	require.Equal(t, []string{"model-a"}, result[0].Models)
	require.Len(t, result[0].MlNodes, 1)
	require.Empty(t, computed.freshNodeIDs, "preserved-only computation must not report fresh PoC provenance")
	require.Len(t, result[0].MlNodes[0].MlNodes, 1)
	require.Equal(t, "node-1", result[0].MlNodes[0].MlNodes[0].NodeId)
}

func TestComputeNewWeightsDropsPreservedOnlyParticipantWithoutSeed(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	currentEpoch := types.Epoch{Index: 1, PocStartBlockHeight: 50}
	require.NoError(t, k.SetEpoch(ctx, &currentEpoch))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpoch.Index))

	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:          currentEpoch.Index,
		PocStartBlockHeight: uint64(currentEpoch.PocStartBlockHeight),
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: testutil.Executor, Weight: 30},
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

	result := am.ComputeNewWeights(ctx, types.Epoch{Index: 2, PocStartBlockHeight: 100})
	require.Empty(t, result)
}

// carryingEpoch starts its PoC while the shards these tests reserve at height 10 still run
var carryingEpoch = types.Epoch{Index: 6, PocStartBlockHeight: 100}

func TestMergeReservedNodesIntoPreservedCarriesFrozenSnapshot(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:        testutil.Executor,
		Address:      testutil.Executor,
		ValidatorKey: "validator-key",
		InferenceUrl: "http://executor",
		Status:       types.ParticipantStatus_ACTIVE,
	}))

	require.NoError(t, k.Trainshards.Set(ctx, 1, types.Trainshard{
		TrainshardId:    1,
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		CreatedAtHeight: 10,
		ExpiresAtHeight: 1000,
		Nodes: []*types.TrainshardReservedNode{
			{Participant: testutil.Executor, NodeId: "node-1", ModelId: "model-a", PocWeight: 7},
			{Participant: testutil.Executor, NodeId: "node-2", ModelId: "model-a", PocWeight: 5},
		},
	}))

	result := am.mergeReservedNodesIntoPreserved(ctx, carryingEpoch, nil)
	require.Len(t, result, 1)
	require.Equal(t, testutil.Executor, result[0].Index)
	require.Equal(t, int64(12), result[0].Weight)
	require.Equal(t, []string{"model-a"}, result[0].Models)
	require.Len(t, result[0].MlNodes, 1)
	require.Len(t, result[0].MlNodes[0].MlNodes, 2)
}

func TestMergeReservedNodesIntoPreservedDedupsExistingNode(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:        testutil.Executor,
		Address:      testutil.Executor,
		ValidatorKey: "validator-key",
		InferenceUrl: "http://executor",
		Status:       types.ParticipantStatus_ACTIVE,
	}))
	require.NoError(t, k.Trainshards.Set(ctx, 1, types.Trainshard{
		TrainshardId:    1,
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		CreatedAtHeight: 10,
		ExpiresAtHeight: 1000,
		Nodes: []*types.TrainshardReservedNode{
			{Participant: testutil.Executor, NodeId: "node-1", ModelId: "model-a", PocWeight: 7},
		},
	}))

	preserved := []*types.ActiveParticipant{{
		Index:   testutil.Executor,
		Models:  []string{"model-a"},
		MlNodes: []*types.ModelMLNodes{{MlNodes: []*types.MLNodeInfo{{NodeId: "node-1", PocWeight: 3}}}},
	}}

	result := am.mergeReservedNodesIntoPreserved(ctx, carryingEpoch, preserved)
	require.Len(t, result, 1)
	require.Len(t, result[0].MlNodes[0].MlNodes, 1)
	require.Equal(t, int64(7), result[0].MlNodes[0].MlNodes[0].PocWeight)
	require.Equal(t, int64(7), result[0].Weight)
}

func TestMergeReservedNodesIntoPreservedCountsANodeUnderOneModel(t *testing.T) {
	for _, weights := range [][2]int64{{10, 20}, {20, 10}} {
		k, ctx := newMinimalInferenceKeeper(t)
		am := NewAppModule(nil, k, nil, nil, nil, nil)

		require.NoError(t, k.SetParticipant(ctx, types.Participant{
			Index:        testutil.Executor,
			Address:      testutil.Executor,
			ValidatorKey: "validator-key",
			InferenceUrl: "http://executor",
			Status:       types.ParticipantStatus_ACTIVE,
		}))
		require.NoError(t, k.Trainshards.Set(ctx, 1, types.Trainshard{
			TrainshardId:    1,
			Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
			CreatedAtHeight: 10,
			ExpiresAtHeight: 1000,
			Nodes: []*types.TrainshardReservedNode{
				{Participant: testutil.Executor, NodeId: "node-1", ModelId: "model-a", PocWeight: weights[0]},
				{Participant: testutil.Executor, NodeId: "node-1", ModelId: "model-b", PocWeight: weights[1]},
			},
		}))

		result := am.mergeReservedNodesIntoPreserved(ctx, carryingEpoch, nil)
		require.Len(t, result, 1)
		require.Equal(t, []string{"model-a"}, result[0].Models)
		require.Len(t, result[0].MlNodes, 1)
		require.Len(t, result[0].MlNodes[0].MlNodes, 1)
		require.Equal(t, weights[0], result[0].Weight)
	}
}

// epoch 1 runs 50..99 and the PoC of epoch 2 starts at 100. The node is reserved at 52 with frozen
// weight 7 and back at 60: what it shows at 100 is its weight, not the frozen one
func TestANodeReturnedBeforeThePoCIsWeighedByItsFreshPoC(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)
	ending := types.Epoch{Index: 1, PocStartBlockHeight: 50}
	upcoming := types.Epoch{Index: 2, PocStartBlockHeight: 100}
	require.NoError(t, k.SetEpoch(ctx, &ending))
	require.NoError(t, k.SetEpoch(ctx, &upcoming))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, ending.Index))
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index: testutil.Executor, Address: testutil.Executor, ValidatorKey: "validator-key",
		InferenceUrl: "http://executor", Status: types.ParticipantStatus_ACTIVE,
	}))
	require.NoError(t, k.Trainshards.Set(ctx, 1, types.Trainshard{
		TrainshardId: 1, Status: types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED,
		CreatedAtHeight: 52, ExpiresAtHeight: 1000, ClosedAtHeight: 55,
		Nodes: []*types.TrainshardReservedNode{{
			Participant: testutil.Executor, NodeId: "node-1", ModelId: "model-a", PocWeight: 7,
			Status:           types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_RELEASED_ON_CLOSE,
			ReleasedAtHeight: 55, ReservedUntilHeight: 60,
		}},
	}))

	keyA := types.PoCParticipantModelKey{ParticipantAddress: testutil.Executor, ModelID: "model-a"}
	keyB := types.PoCParticipantModelKey{ParticipantAddress: testutil.Executor, ModelID: "model-b"}
	commits := map[types.PoCParticipantModelKey]types.PoCV2StoreCommit{keyA: {Count: 80}, keyB: {Count: 30}}
	distributions := map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution{
		keyA: {Weights: []*types.MLNodeWeight{{NodeId: "node-1", Weight: 80}}},
		keyB: {Weights: []*types.MLNodeWeight{{NodeId: "node-1", Weight: 30}}},
	}
	kept, _ := am.filterStoreCommitsFromInferenceNodes(commits, distributions, am.getInferenceServingNodeIds(ctx, upcoming))

	require.Equal(t, uint32(80), kept[keyA].Count)
	require.Equal(t, uint32(30), kept[keyB].Count)
	require.Empty(t, am.mergeReservedNodesIntoPreserved(ctx, upcoming, nil))
}

// The frozen weight is carried exactly when the fresh PoC is dropped: one without the other would
// leave the node with no weight or with both
func TestEpochBoundaryReservationViewsStaySymmetric(t *testing.T) {
	released := types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_RELEASED_ON_CLOSE
	active := types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE
	cases := []struct {
		name              string
		status            types.TrainshardNodeStatus
		released, until   int64
		keepsFrozenWeight bool
	}{
		{"released and returned before the PoC, its fresh PoC counts", released, 60, 70, false},
		{"still returning when the PoC starts, it may not have made it", released, 95, 115, true},
		{"its return ends in the PoC start block, it still counts as returning", released, 80, 100, true},
		{"still training when the PoC starts", active, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx := newMinimalInferenceKeeper(t)
			am := NewAppModule(nil, k, nil, nil, nil, nil)

			endingEpoch := types.Epoch{Index: 1, PocStartBlockHeight: 50}
			upcomingEpoch := types.Epoch{Index: 2, PocStartBlockHeight: 100}
			require.NoError(t, k.SetEpoch(ctx, &endingEpoch))
			require.NoError(t, k.SetEpoch(ctx, &upcomingEpoch))
			require.NoError(t, k.SetEffectiveEpochIndex(ctx, endingEpoch.Index))
			require.NoError(t, k.SetParticipant(ctx, types.Participant{
				Index:        testutil.Executor,
				Address:      testutil.Executor,
				ValidatorKey: "validator-key",
				InferenceUrl: "http://executor",
				Status:       types.ParticipantStatus_ACTIVE,
			}))
			shard := types.Trainshard{
				TrainshardId:    1,
				Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
				CreatedAtHeight: 55,
				ExpiresAtHeight: 1000,
				Nodes: []*types.TrainshardReservedNode{{
					Participant:         testutil.Executor,
					NodeId:              "node-1",
					ModelId:             "model-a",
					PocWeight:           7,
					Status:              tc.status,
					ReleasedAtHeight:    tc.released,
					ReservedUntilHeight: tc.until,
				}},
			}
			if tc.status == released {
				shard.Status = types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED
				shard.ClosedAtHeight = tc.released
			}
			require.NoError(t, k.Trainshards.Set(ctx, 1, shard))

			key := types.PoCParticipantModelKey{ParticipantAddress: testutil.Executor, ModelID: "model-a"}
			kept, _ := am.filterStoreCommitsFromInferenceNodes(
				map[types.PoCParticipantModelKey]types.PoCV2StoreCommit{key: {Count: 80}},
				map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution{key: {Weights: []*types.MLNodeWeight{{NodeId: "node-1", Weight: 80}}}},
				am.getInferenceServingNodeIds(ctx, upcomingEpoch),
			)
			merged := am.mergeReservedNodesIntoPreserved(ctx, upcomingEpoch, nil)

			_, freshKept := kept[key]
			require.Equal(t, !tc.keepsFrozenWeight, freshKept)
			if !tc.keepsFrozenWeight {
				require.Empty(t, merged)
				return
			}
			require.Len(t, merged, 1)
			require.Equal(t, int64(7), merged[0].Weight)
		})
	}
}

func TestComputeNewWeightsCarriesReservedOnlyHostWithSeed(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	currentEpoch := types.Epoch{Index: 1, PocStartBlockHeight: 50}
	require.NoError(t, k.SetEpoch(ctx, &currentEpoch))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpoch.Index))

	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:        testutil.Executor,
		Address:      testutil.Executor,
		ValidatorKey: "validator-key",
		InferenceUrl: "http://executor",
		Status:       types.ParticipantStatus_ACTIVE,
	}))
	require.NoError(t, k.Trainshards.Set(ctx, 1, types.Trainshard{
		TrainshardId:    1,
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		CreatedAtHeight: 60,
		ExpiresAtHeight: 1000,
		Nodes: []*types.TrainshardReservedNode{
			{Participant: testutil.Executor, NodeId: "node-1", ModelId: "model-a", PocWeight: 9},
		},
	}))
	require.NoError(t, k.SetRandomSeed(ctx, types.RandomSeed{
		Participant: testutil.Executor,
		EpochIndex:  2,
		Signature:   "seed-signature",
	}))

	result := am.ComputeNewWeights(ctx, types.Epoch{Index: 2, PocStartBlockHeight: 100})
	require.Len(t, result, 1)
	require.Equal(t, testutil.Executor, result[0].Index)
	require.Equal(t, int64(9), result[0].Weight)
}

func TestComputeNewWeightsDropsReservedOnlyHostWithoutSeed(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	currentEpoch := types.Epoch{Index: 1, PocStartBlockHeight: 50}
	require.NoError(t, k.SetEpoch(ctx, &currentEpoch))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpoch.Index))

	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:        testutil.Executor,
		Address:      testutil.Executor,
		ValidatorKey: "validator-key",
		InferenceUrl: "http://executor",
		Status:       types.ParticipantStatus_ACTIVE,
	}))
	require.NoError(t, k.Trainshards.Set(ctx, 1, types.Trainshard{
		TrainshardId:    1,
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		CreatedAtHeight: 60,
		ExpiresAtHeight: 1000,
		Nodes: []*types.TrainshardReservedNode{
			{Participant: testutil.Executor, NodeId: "node-1", ModelId: "model-a", PocWeight: 9},
		},
	}))

	result := am.ComputeNewWeights(ctx, types.Epoch{Index: 2, PocStartBlockHeight: 100})
	require.Empty(t, result)
}
