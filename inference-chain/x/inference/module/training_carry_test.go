package inference

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
)

var carryUpcomingEpoch = types.Epoch{Index: 6, PocStartBlockHeight: 600}

type carryModelNodes map[string][]*types.MLNodeInfo

func carryParticipant(addr string, modelNodes carryModelNodes) *types.ActiveParticipant {
	p := &types.ActiveParticipant{Index: addr}
	for _, model := range []string{"model-a", "model-b"} {
		nodes, ok := modelNodes[model]
		if !ok {
			continue
		}
		p.Models = append(p.Models, model)
		p.MlNodes = append(p.MlNodes, &types.ModelMLNodes{MlNodes: nodes})
	}
	p.Weight = RecalculateWeight(p)
	return p
}

func carryNodesOf(weights ...int64) []*types.MLNodeInfo {
	nodes := make([]*types.MLNodeInfo, len(weights))
	for i, w := range weights {
		nodes[i] = &types.MLNodeInfo{NodeId: "n" + string(rune('1'+i)), PocWeight: w}
	}
	return nodes
}

func freeNodesOf(weights ...int64) []*types.MLNodeInfo {
	nodes := carryNodesOf(append([]int64{0}, weights...)...)
	return nodes[1:]
}

type carrySetup struct {
	current      *types.ActiveParticipant
	confirmation int64
	scales       []*types.ConfirmationWeightScale
	record       int64
	inRoot       bool
	unanchored   bool
}

func setupTrainingCarry(t *testing.T, s carrySetup) (AppModule, sdk.Context) {
	t.Helper()
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: 5, PocStartBlockHeight: 500}))
	require.NoError(t, k.SetEpoch(ctx, &carryUpcomingEpoch))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      5,
		Participants: []*types.ActiveParticipant{s.current},
	}))
	root := types.EpochGroupData{EpochIndex: 5, ConfirmationWeightScales: s.scales}
	if s.inRoot {
		root.ValidationWeights = []*types.ValidationWeight{{
			MemberAddress:      s.current.Index,
			Weight:             s.current.Weight,
			ConfirmationWeight: s.confirmation,
		}}
	}
	k.SetEpochGroupData(ctx, root)
	require.NoError(t, k.Trainshards.Set(ctx, 1, types.Trainshard{
		TrainshardId:    1,
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		CreatedAtHeight: 510,
		ExpiresAtHeight: 5000,
		Nodes: []*types.TrainshardReservedNode{{
			Participant: s.current.Index,
			NodeId:      "n1",
			ModelId:     "model-a",
			PocWeight:   s.record,
			Status:      types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE,
		}},
	}))
	if !s.unanchored {
		require.NoError(t, k.TrainshardWeightEpoch.Set(ctx, 1, 5))
	}
	return am, ctx
}

func carriedNodeWeight(p *types.ActiveParticipant, nodeId string) int64 {
	for _, group := range p.MlNodes {
		for _, n := range group.MlNodes {
			if n.NodeId == nodeId {
				return n.PocWeight
			}
		}
	}
	return -1
}

func coefficientScales(coefficients map[string]float64) []*types.ConfirmationWeightScale {
	scales := make([]*types.ConfirmationWeightScale, 0, len(coefficients))
	for _, model := range []string{"model-a", "model-b"} {
		if c, ok := coefficients[model]; ok {
			scales = append(scales, &types.ConfirmationWeightScale{ModelId: model, EffectiveCoefficient: types.DecimalFromFloat(c)})
		}
	}
	return scales
}

func TestTrainingCarryBudget(t *testing.T) {
	host := testutil.Executor
	cases := []struct {
		name         string
		current      carryModelNodes
		confirmation int64
		scales       []*types.ConfirmationWeightScale
		record       int64
		notInRoot    bool
		unanchored   bool
		upcoming     carryModelNodes
		wantCarry    int64
		wantWeight   int64
	}{
		{
			name:    "an honest host keeps its frozen weight",
			current: carryModelNodes{"model-a": carryNodesOf(100, 100, 100, 100)}, record: 100,
			upcoming:  carryModelNodes{"model-a": append(carryNodesOf(100), freeNodesOf(100, 100, 100)...)},
			wantCarry: 100, wantWeight: 400,
		},
		{
			name:    "a node declared 30% heavy gains nothing",
			current: carryModelNodes{"model-a": carryNodesOf(130, 90, 90, 90)}, record: 130,
			upcoming:  carryModelNodes{"model-a": append(carryNodesOf(130), freeNodesOf(100, 100, 100)...)},
			wantCarry: 100, wantWeight: 400,
		},
		{
			name:    "a 400/0/0/0 split gains nothing",
			current: carryModelNodes{"model-a": carryNodesOf(400, 0, 0, 0)}, record: 130,
			upcoming:  carryModelNodes{"model-a": append(carryNodesOf(130), freeNodesOf(100, 100, 100)...)},
			wantCarry: 100, wantWeight: 400,
		},
		{
			name:    "sending the other nodes home leaves only the capped record",
			current: carryModelNodes{"model-a": carryNodesOf(400, 0, 0, 0)}, record: 130,
			upcoming:  carryModelNodes{"model-a": carryNodesOf(130)},
			wantCarry: 130, wantWeight: 130,
		},
		{
			name:         "a cPoC cut shrinks the budget",
			current:      carryModelNodes{"model-a": carryNodesOf(100, 100, 100, 100)},
			confirmation: 200, scales: coefficientScales(map[string]float64{"model-a": 1}), record: 100,
			upcoming:  carryModelNodes{"model-a": append(carryNodesOf(100), freeNodesOf(100, 100, 100)...)},
			wantCarry: 0, wantWeight: 300,
		},
		{
			name:    "a fallback carry of the epoch weights is held to the record",
			current: carryModelNodes{"model-a": carryNodesOf(400, 0, 0, 0)}, record: 130,
			upcoming:  carryModelNodes{"model-a": carryNodesOf(400, 0, 0, 0)},
			wantCarry: 130, wantWeight: 130,
		},
		{
			name:    "a second PoC of the lease carries no more than the first did",
			current: carryModelNodes{"model-a": carryNodesOf(60, 100, 100, 100)}, record: 130,
			upcoming:  carryModelNodes{"model-a": append(carryNodesOf(130), freeNodesOf(100, 100, 100)...)},
			wantCarry: 60, wantWeight: 360,
		},
		{
			name:         "moving free nodes to another model is weighed by its coefficient",
			current:      carryModelNodes{"model-a": carryNodesOf(400, 0, 0, 0)},
			confirmation: 400, scales: coefficientScales(map[string]float64{"model-a": 1, "model-b": 2}), record: 130,
			upcoming:  carryModelNodes{"model-a": carryNodesOf(130), "model-b": freeNodesOf(50, 50, 50)},
			wantCarry: 100, wantWeight: 250,
		},
		{
			name:    "a host the current epoch does not hold carries nothing",
			current: carryModelNodes{"model-a": carryNodesOf(100, 100, 100, 100)}, record: 100, notInRoot: true,
			upcoming:  carryModelNodes{"model-a": append(carryNodesOf(100), freeNodesOf(100, 100, 100)...)},
			wantCarry: 0, wantWeight: 300,
		},
		{
			name:    "a lease without a weight epoch carries nothing",
			current: carryModelNodes{"model-a": carryNodesOf(100, 100, 100, 100)}, record: 100, unanchored: true,
			upcoming:  carryModelNodes{"model-a": append(carryNodesOf(100), freeNodesOf(100, 100, 100)...)},
			wantCarry: 0, wantWeight: 300,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			am, ctx := setupTrainingCarry(t, carrySetup{
				current:      carryParticipant(host, tc.current),
				confirmation: tc.confirmation,
				scales:       tc.scales,
				record:       tc.record,
				inRoot:       !tc.notInRoot,
				unanchored:   tc.unanchored,
			})
			upcoming := carryParticipant(host, tc.upcoming)

			am.buildTrainingCarryBudget(ctx, carryUpcomingEpoch).apply([]*types.ActiveParticipant{upcoming})

			require.Equal(t, tc.wantCarry, carriedNodeWeight(upcoming, "n1"))
			require.Equal(t, tc.wantWeight, upcoming.Weight)
		})
	}
}

func TestTrainingCarryBudget_MergedCarryStartsFromTheRecord(t *testing.T) {
	am, ctx := setupTrainingCarry(t, carrySetup{
		current: carryParticipant(testutil.Executor, carryModelNodes{"model-a": carryNodesOf(60, 100)}),
		record:  130,
		inRoot:  true,
	})
	require.NoError(t, am.keeper.SetParticipant(ctx, types.Participant{
		Index: testutil.Executor, Address: testutil.Executor, ValidatorKey: "validator-key",
		Status: types.ParticipantStatus_ACTIVE,
	}))

	merged := am.mergeReservedNodesIntoPreserved(ctx, carryUpcomingEpoch, nil)
	require.Len(t, merged, 1)
	require.Equal(t, int64(130), carriedNodeWeight(merged[0], "n1"))
}

func TestSeatAndGuard_DropsAHostWhoseOnlyWeightIsAnUnbackedCarry(t *testing.T) {
	cheater := testutil.Executor
	honest := testutil.Executor2
	am, ctx := setupTrainingCarry(t, carrySetup{
		current:      carryParticipant(cheater, carryModelNodes{"model-a": carryNodesOf(400, 0, 0, 0)}),
		confirmation: 0,
		scales:       coefficientScales(map[string]float64{"model-a": 1}),
		record:       130,
		inRoot:       true,
	})
	am.keeper.SetModel(ctx, &types.Model{ProposedBy: "genesis", Id: "model-a"})

	fresh := []*types.ActiveParticipant{
		carryParticipant(honest, carryModelNodes{"model-a": []*types.MLNodeInfo{{NodeId: "h1", PocWeight: 100}}}),
		carryParticipant(cheater, carryModelNodes{"model-a": carryNodesOf(130)}),
	}
	result, usedFallback := am.seatAndGuardParticipants(ctx, carryUpcomingEpoch, fresh, participantNodeIDs(fresh[:1]))

	require.False(t, usedFallback)
	require.Len(t, result, 1)
	require.Equal(t, honest, result[0].Index)
}

func TestTrainingCarryBudget_FreeNodeNoiseDoesNotWearDownTheHost(t *testing.T) {
	host := testutil.Executor
	scales := coefficientScales(map[string]float64{"model-a": 1})
	am, ctx := setupTrainingCarry(t, carrySetup{
		current:      carryParticipant(host, carryModelNodes{"model-a": carryNodesOf(8946, 7154)}),
		confirmation: 16100,
		scales:       scales,
		record:       8946,
		inRoot:       true,
	})

	carried := int64(8946)
	for i, free := range []int64{7303, 7303, 7214, 7214, 7333, 7184, 7363} {
		epoch := types.Epoch{
			Index:               carryUpcomingEpoch.Index + uint64(i),
			PocStartBlockHeight: carryUpcomingEpoch.PocStartBlockHeight + int64(i)*100,
		}
		require.NoError(t, am.keeper.SetEpoch(ctx, &epoch))
		upcoming := carryParticipant(host, carryModelNodes{"model-a": carryNodesOf(carried, free)})

		am.buildTrainingCarryBudget(ctx, epoch).apply([]*types.ActiveParticipant{upcoming})

		require.Equal(t, int64(16100), upcoming.Weight, "epoch %d", epoch.Index)
		carried = carriedNodeWeight(upcoming, "n1")
		require.NoError(t, am.keeper.SetActiveParticipants(ctx, types.ActiveParticipants{
			EpochId:      epoch.Index,
			Participants: []*types.ActiveParticipant{upcoming},
		}))
		am.keeper.SetEpochGroupData(ctx, types.EpochGroupData{
			EpochIndex:               epoch.Index,
			ConfirmationWeightScales: scales,
			ValidationWeights: []*types.ValidationWeight{{
				MemberAddress:      host,
				Weight:             upcoming.Weight,
				ConfirmationWeight: upcoming.Weight * 999 / 1000,
			}},
		})
	}
}

func TestTrainingCarryBudget_OverlappingLeasesKeepTheEarliestAnchor(t *testing.T) {
	host := testutil.Executor
	am, ctx := setupTrainingCarry(t, carrySetup{
		current: carryParticipant(host, carryModelNodes{"model-a": carryNodesOf(60, 100, 100, 100)}),
		record:  100,
		inRoot:  true,
	})
	before := carryParticipant(host, carryModelNodes{"model-a": carryNodesOf(100, 100, 100, 100)})
	require.NoError(t, am.keeper.SetEpoch(ctx, &types.Epoch{Index: 3, PocStartBlockHeight: 300}))
	require.NoError(t, am.keeper.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      3,
		Participants: []*types.ActiveParticipant{before},
	}))
	am.keeper.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:        3,
		ValidationWeights: []*types.ValidationWeight{{MemberAddress: host, Weight: before.Weight}},
	})
	require.NoError(t, am.keeper.Trainshards.Set(ctx, 2, types.Trainshard{
		TrainshardId:    2,
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		CreatedAtHeight: 310,
		ExpiresAtHeight: 5000,
		Nodes: []*types.TrainshardReservedNode{{
			Participant: host,
			NodeId:      "n2",
			ModelId:     "model-a",
			PocWeight:   100,
			Status:      types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE,
		}},
	}))
	require.NoError(t, am.keeper.TrainshardWeightEpoch.Set(ctx, 2, 3))
	upcoming := carryParticipant(host, carryModelNodes{"model-a": carryNodesOf(100, 100, 100, 100)})

	am.buildTrainingCarryBudget(ctx, carryUpcomingEpoch).apply([]*types.ActiveParticipant{upcoming})

	require.Equal(t, int64(100), carriedNodeWeight(upcoming, "n1"))
	require.Equal(t, int64(100), carriedNodeWeight(upcoming, "n2"))
	require.Equal(t, int64(400), upcoming.Weight)
}
