package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

type weightCapNode struct {
	id       string
	weight   int64
	hardware []*types.Hardware
}

type weightCapHost struct {
	addr  string
	nodes []weightCapNode
}

func h100Nodes(weights ...int64) []weightCapNode {
	nodes := make([]weightCapNode, len(weights))
	for i, w := range weights {
		nodes[i] = weightCapNode{
			id:       "node-" + string(rune('a'+i)),
			weight:   w,
			hardware: []*types.Hardware{{Type: "NVIDIA H100", Count: 8}},
		}
	}
	return nodes
}

func setupNodeWeightCap(t *testing.T, minParticipants uint32, hosts ...weightCapHost) (keeper.Keeper, types.MsgServer, sdk.Context) {
	t.Helper()
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	ms := keeper.NewMsgServerImpl(k)
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	params := types.DefaultParams()
	params.TrainingParams.TrainingEnabled = true
	params.TrainingParams.MaxReservedSharePerModelBps = 10000
	params.TrainingParams.MaxReservedSharePerProfileBps = 10000
	params.TrainingParams.MinWeightRateParticipants = minParticipants
	require.NoError(t, k.SetParams(ctx, params))

	const epoch = uint64(7)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epoch))
	participants := make([]*types.ActiveParticipant, 0, len(hosts))
	for _, host := range hosts {
		require.NoError(t, k.SetParticipant(ctx, types.Participant{Index: host.addr, Address: host.addr}))
		mlNodes := make([]*types.MLNodeInfo, 0, len(host.nodes))
		hardware := make([]*types.HardwareNode, 0, len(host.nodes))
		for _, n := range host.nodes {
			mlNodes = append(mlNodes, &types.MLNodeInfo{NodeId: n.id, PocWeight: n.weight})
			hardware = append(hardware, &types.HardwareNode{LocalId: n.id, Hardware: n.hardware})
		}
		participants = append(participants, &types.ActiveParticipant{
			Index:   host.addr,
			Models:  []string{"model1"},
			MlNodes: []*types.ModelMLNodes{{MlNodes: mlNodes}},
		})
		require.NoError(t, k.SetHardwareNodes(ctx, &types.HardwareNodes{Participant: host.addr, HardwareNodes: hardware}))
	}
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{EpochId: epoch, Participants: participants}))

	target := hosts[0]
	require.NoError(t, k.TrainingNodeOptIns.Set(ctx, collections.Join(target.addr, target.nodes[0].id), trainshardOptInExpiry))
	require.NoError(t, k.TrainingNodeEndpoints.Set(ctx, collections.Join(target.addr, target.nodes[0].id), "https://a.example.com"))
	require.NoError(t, k.TrainshardProposals.Set(ctx, 1, types.TrainshardProposal{
		Creator:           target.addr,
		GpuProfileId:      trainshardTestProfile,
		NodeCount:         1,
		MaxDurationBlocks: 100,
		Id:                1,
		Status:            types.TrainshardProposalStatus_TRAINSHARD_PROPOSAL_STATUS_OPEN,
	}))
	return k, ms, ctx.WithBlockHeight(50)
}

func assembledWeight(t *testing.T, k keeper.Keeper, ms types.MsgServer, ctx sdk.Context, creator string) int64 {
	t.Helper()
	resp, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.NoError(t, err)
	shard, err := k.Trainshards.Get(ctx, resp.TrainshardId)
	require.NoError(t, err)
	require.Len(t, shard.Nodes, 1)
	require.Equal(t, "node-a", shard.Nodes[0].NodeId)
	return shard.Nodes[0].PocWeight
}

func TestAssembleTrainshard_CapsAnInflatedNodeAtTheMedianRate(t *testing.T) {
	cheater := weightCapHost{addr: sample.AccAddress(), nodes: h100Nodes(400, 0, 0, 0)}
	k, ms, ctx := setupNodeWeightCap(t, 3,
		cheater,
		weightCapHost{addr: sample.AccAddress(), nodes: h100Nodes(100, 100, 100, 100)},
		weightCapHost{addr: sample.AccAddress(), nodes: h100Nodes(100, 100, 100, 100)},
	)

	require.Equal(t, int64(130), assembledWeight(t, k, ms, ctx, cheater.addr))
}

func TestAssembleTrainshard_KeepsAnHonestNodeWeight(t *testing.T) {
	honest := weightCapHost{addr: sample.AccAddress(), nodes: h100Nodes(110, 90, 100, 100)}
	k, ms, ctx := setupNodeWeightCap(t, 3,
		honest,
		weightCapHost{addr: sample.AccAddress(), nodes: h100Nodes(100, 100, 100, 100)},
		weightCapHost{addr: sample.AccAddress(), nodes: h100Nodes(100, 100, 100, 100)},
	)

	require.Equal(t, int64(110), assembledWeight(t, k, ms, ctx, honest.addr))
}

func TestAssembleTrainshard_TheMedianCountsEachParticipantOnce(t *testing.T) {
	cheater := weightCapHost{addr: sample.AccAddress(), nodes: h100Nodes(400, 0, 0, 0)}
	k, ms, ctx := setupNodeWeightCap(t, 3,
		cheater,
		weightCapHost{addr: sample.AccAddress(), nodes: h100Nodes(300, 300, 300, 300, 300, 300, 300, 300, 300, 300)},
		weightCapHost{addr: sample.AccAddress(), nodes: h100Nodes(100, 100, 100, 100)},
	)

	require.Equal(t, int64(130), assembledWeight(t, k, ms, ctx, cheater.addr))
}

func TestAssembleTrainshard_RefusesWithoutEnoughRateParticipants(t *testing.T) {
	cheater := weightCapHost{addr: sample.AccAddress(), nodes: h100Nodes(400, 0, 0, 0)}
	k, ms, ctx := setupNodeWeightCap(t, 3,
		cheater,
		weightCapHost{addr: sample.AccAddress(), nodes: h100Nodes(100, 100, 100, 100)},
	)

	_, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: cheater.addr, ProposalId: 1})
	require.ErrorIs(t, err, types.ErrTrainshardCapacity)
	require.ErrorContains(t, err, "1 without a reliable weight rate (3 participants needed)")
	require.Empty(t, k.CollectReservedNodeIds(ctx))
}

func TestAssembleTrainshard_MixedGpuParticipantsStayOutOfTheMedian(t *testing.T) {
	mixed := h100Nodes(100, 100, 100, 100)
	mixed[3].hardware = []*types.Hardware{{Type: "NVIDIA A100", Count: 8}}
	cheater := weightCapHost{addr: sample.AccAddress(), nodes: h100Nodes(400, 0, 0, 0)}
	hosts := []weightCapHost{
		cheater,
		{addr: sample.AccAddress(), nodes: h100Nodes(100, 100, 100, 100)},
		{addr: sample.AccAddress(), nodes: mixed},
	}

	_, ms, ctx := setupNodeWeightCap(t, 3, hosts...)
	_, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: cheater.addr, ProposalId: 1})
	require.ErrorIs(t, err, types.ErrTrainshardCapacity)
	require.ErrorContains(t, err, "1 without a reliable weight rate")

	hosts = append(hosts, weightCapHost{addr: sample.AccAddress(), nodes: h100Nodes(100, 100, 100, 100)})
	k, ms, ctx := setupNodeWeightCap(t, 3, hosts...)
	require.Equal(t, int64(130), assembledWeight(t, k, ms, ctx, cheater.addr))
}

func setupReservedWeightClamp(t *testing.T, epochNodes []*types.MLNodeInfo, reserved ...*types.TrainshardReservedNode) (keeper.Keeper, sdk.Context, string) {
	t.Helper()
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	const epoch = uint64(7)
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: epoch, PocStartBlockHeight: 100}))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: epoch + 1, PocStartBlockHeight: 200}))

	host := sample.AccAddress()
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: epoch,
		Participants: []*types.ActiveParticipant{{
			Index:   host,
			Models:  []string{"model1"},
			MlNodes: []*types.ModelMLNodes{{MlNodes: epochNodes}},
		}},
	}))
	for _, n := range reserved {
		n.Participant = host
		n.ModelId = "model1"
		n.Status = types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE
	}
	require.NoError(t, k.Trainshards.Set(ctx, 1, types.Trainshard{
		TrainshardId:    1,
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		CreatedAtHeight: 110,
		Nodes:           reserved,
	}))
	return k, ctx.WithBlockHeight(150), host
}

func TestReservedWeight_AnInflatedSplitLeavesTheRestAsFreeWeight(t *testing.T) {
	k, ctx, host := setupReservedWeightClamp(t,
		[]*types.MLNodeInfo{{NodeId: "n1", PocWeight: 400}, {NodeId: "n2"}, {NodeId: "n3"}, {NodeId: "n4"}},
		&types.TrainshardReservedNode{NodeId: "n1", PocWeight: 130},
	)

	_, byHost := k.CollectEpochReservedWeightTotals(ctx, 7, keeper.ReservationScopeReward)
	require.Equal(t, int64(130), byHost[host])
	require.Equal(t, int64(270), keeper.CollectModelFreeWeightsForTesting(k, ctx, 7, "model1")[host])
	require.False(t, k.BuildEpochReservationView(ctx, 7).FullyReservedAt(host, 150))
}

func TestReservedWeight_OmittedNodesDoNotMakeTheHostFullyReserved(t *testing.T) {
	k, ctx, host := setupReservedWeightClamp(t,
		[]*types.MLNodeInfo{{NodeId: "n1", PocWeight: 400}},
		&types.TrainshardReservedNode{NodeId: "n1", PocWeight: 130},
	)

	require.False(t, k.BuildEpochReservationView(ctx, 7).FullyReservedAt(host, 150))
	require.Equal(t, int64(270), keeper.CollectModelFreeWeightsForTesting(k, ctx, 7, "model1")[host])
	shielded := k.CollectEpochReservedNodeWeightsBetween(ctx, 7, 120, 180, keeper.ReservationScopeShield)
	require.Len(t, shielded[host], 1)
	require.Equal(t, int64(130), shielded[host][0].PocWeight)
}

func TestReservedWeight_AnHonestFullyReservedHostIsStillFullyReserved(t *testing.T) {
	k, ctx, host := setupReservedWeightClamp(t,
		[]*types.MLNodeInfo{{NodeId: "n1", PocWeight: 100}},
		&types.TrainshardReservedNode{NodeId: "n1", PocWeight: 100},
	)

	require.True(t, k.BuildEpochReservationView(ctx, 7).FullyReservedAt(host, 150))
	require.Zero(t, keeper.CollectModelFreeWeightsForTesting(k, ctx, 7, "model1")[host])
}

func TestReservedWeight_TheEpochWeightBoundsTheRecord(t *testing.T) {
	k, ctx, host := setupReservedWeightClamp(t,
		[]*types.MLNodeInfo{{NodeId: "n1", PocWeight: 50}, {NodeId: "n2", PocWeight: 100}},
		&types.TrainshardReservedNode{NodeId: "n1", PocWeight: 130},
		&types.TrainshardReservedNode{NodeId: "gone", PocWeight: 130},
	)

	weights := make(map[string]int64)
	for _, n := range k.CollectEpochReservedNodeWeights(ctx, 7, keeper.ReservationScopeReward)[host] {
		weights[n.NodeId] = n.PocWeight
	}
	require.Equal(t, map[string]int64{"n1": 50, "gone": 0}, weights)

	records := make(map[string]int64)
	for _, n := range k.CollectEpochReservations(ctx, 7).Nodes[host] {
		records[n.NodeId] = n.PocWeight
	}
	require.Equal(t, map[string]int64{"n1": 130, "gone": 130}, records)
}

func TestReservedWeight_AZeroWeightReservationShieldsNoDevshardHost(t *testing.T) {
	k, ctx, host := setupReservedWeightClamp(t,
		[]*types.MLNodeInfo{{NodeId: "n1"}, {NodeId: "n2", PocWeight: 100}},
		&types.TrainshardReservedNode{NodeId: "n1", PocWeight: 130},
	)
	require.NotContains(t, k.CollectEpochReservedHostsForModel(ctx, 7, "model1", keeper.ReservationScopeShield), host)

	k, ctx, host = setupReservedWeightClamp(t,
		[]*types.MLNodeInfo{{NodeId: "n1", PocWeight: 100}, {NodeId: "n2", PocWeight: 100}},
		&types.TrainshardReservedNode{NodeId: "n1", PocWeight: 130},
	)
	require.Contains(t, k.CollectEpochReservedHostsForModel(ctx, 7, "model1", keeper.ReservationScopeShield), host)
}

func TestSubmitHardwareDiff_RejectsChangingANodeInItsReturnBuffer(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)
	resp, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.NoError(t, err)
	_, err = ms.SettleTrainshard(ctx, &types.MsgSettleTrainshard{Creator: creator, TrainshardId: resp.TrainshardId})
	require.NoError(t, err)
	shard, err := k.Trainshards.Get(ctx, resp.TrainshardId)
	require.NoError(t, err)
	node := shard.Nodes[0]
	require.False(t, k.IsNodeActivelyReserved(ctx, creator, node.NodeId))
	require.True(t, k.IsNodeReserved(ctx, creator, node.NodeId))

	_, err = ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{
		Creator: creator, Removed: []*types.HardwareNode{{LocalId: node.NodeId}},
	})
	require.ErrorIs(t, err, types.ErrTrainshardNodeReserved)
	_, err = ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{
		Creator: creator, NewOrModified: []*types.HardwareNode{{
			LocalId: node.NodeId, Hardware: []*types.Hardware{{Type: "NVIDIA A100", Count: 8}},
		}},
	})
	require.ErrorIs(t, err, types.ErrTrainshardNodeReserved)

	k.ProcessTrainshardEndBlock(ctx.WithBlockHeight(node.ReservedUntilHeight))
	_, err = ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{
		Creator: creator, Removed: []*types.HardwareNode{{LocalId: node.NodeId}},
	})
	require.NoError(t, err)
}
