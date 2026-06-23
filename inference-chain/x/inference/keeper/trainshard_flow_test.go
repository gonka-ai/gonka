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

const trainshardTestProfile = "NVIDIA H100 x8"

// setupTrainshardFlow seeds two opted-in nodes and an OPEN proposal
func setupTrainshardFlow(t *testing.T, maxNodes uint32) (keeper.Keeper, types.MsgServer, sdk.Context, string) {
	t.Helper()
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	ms := keeper.NewMsgServerImpl(k)
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	params := types.DefaultParams()
	params.TrainingParams.TrainingEnabled = true
	require.NoError(t, k.SetParams(ctx, params))

	creator := sample.AccAddress()
	const epoch = uint64(7)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epoch))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: epoch,
		Participants: []*types.ActiveParticipant{{
			Index:  creator,
			Models: []string{"model1"},
			MlNodes: []*types.ModelMLNodes{{MlNodes: []*types.MLNodeInfo{
				{NodeId: "node-a", PocWeight: 100},
				{NodeId: "node-b", PocWeight: 100},
			}}},
		}},
	}))
	require.NoError(t, k.SetHardwareNodes(ctx, &types.HardwareNodes{
		Participant: creator,
		HardwareNodes: []*types.HardwareNode{
			{LocalId: "node-a", Hardware: []*types.Hardware{{Type: "NVIDIA H100", Count: 8}}},
			{LocalId: "node-b", Hardware: []*types.Hardware{{Type: "NVIDIA H100", Count: 8}}},
		},
	}))
	require.NoError(t, k.TrainingNodeOptIns.Set(ctx, collections.Join(creator, "node-a")))
	require.NoError(t, k.TrainingNodeOptIns.Set(ctx, collections.Join(creator, "node-b")))
	require.NoError(t, k.TrainshardProposals.Set(ctx, 1, types.TrainshardProposal{
		Creator:           creator,
		GpuProfileId:      trainshardTestProfile,
		MaxNodes:          maxNodes,
		MaxDurationBlocks: 100,
		Id:                1,
		Status:            types.TrainshardProposalStatus_TRAINSHARD_PROPOSAL_STATUS_OPEN,
	}))

	return k, ms, ctx.WithBlockHeight(50), creator
}

func TestAssembleAndSettleTrainshard(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)

	resp, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.NoError(t, err)
	require.Equal(t, uint64(1), resp.TrainshardId)

	// capacity buffer keeps one of the two nodes free
	reservedA := k.IsNodeReserved(ctx, creator, "node-a")
	reservedB := k.IsNodeReserved(ctx, creator, "node-b")
	require.NotEqual(t, reservedA, reservedB)

	shard, err := k.Trainshards.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE, shard.Status)
	require.Len(t, shard.Nodes, 1)

	_, err = ms.SettleTrainshard(ctx, &types.MsgSettleTrainshard{Creator: creator, TrainshardId: 1})
	require.NoError(t, err)

	shard, err = k.Trainshards.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED, shard.Status)
	require.False(t, k.IsNodeReserved(ctx, creator, "node-a"))
	require.False(t, k.IsNodeReserved(ctx, creator, "node-b"))
}

// TestDeferredExpiryThenManualSettle checks deferred-expiry settle cleanup
func TestDeferredExpiryThenManualSettle(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)

	_, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.NoError(t, err)

	shard, err := k.Trainshards.Get(ctx, 1)
	require.NoError(t, err)
	planned := shard.ExpiresAtHeight
	retry := planned + 1

	// simulate deferred expiry: index moves, planned height stays
	require.NoError(t, k.TrainshardExpiryIndex.Set(ctx, collections.Join(retry, uint64(1))))
	require.NoError(t, k.TrainshardExpiryIndex.Remove(ctx, collections.Join(planned, uint64(1))))

	_, err = ms.SettleTrainshard(ctx, &types.MsgSettleTrainshard{Creator: creator, TrainshardId: 1})
	require.NoError(t, err)

	shard, err = k.Trainshards.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED, shard.Status)
	require.Equal(t, planned, shard.ExpiresAtHeight)
	require.False(t, k.IsNodeReserved(ctx, creator, "node-a"))
	require.False(t, k.IsNodeReserved(ctx, creator, "node-b"))

	// deferred key is orphaned by settle and cleaned next end-block
	hasRetry, err := k.TrainshardExpiryIndex.Has(ctx, collections.Join(retry, uint64(1)))
	require.NoError(t, err)
	require.True(t, hasRetry)

	k.ProcessTrainshardEndBlock(ctx.WithBlockHeight(retry))

	hasRetry, err = k.TrainshardExpiryIndex.Has(ctx, collections.Join(retry, uint64(1)))
	require.NoError(t, err)
	require.False(t, hasRetry)
}

func TestAssembleTrainshard_RespectsCapacityBuffer(t *testing.T) {
	// requesting both nodes fails because one must stay free
	_, ms, ctx, creator := setupTrainshardFlow(t, 2)

	_, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.ErrorIs(t, err, types.ErrTrainshardCapacity)
}

func TestEpochReservationView_TimeLocalFullReservation(t *testing.T) {
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
			MlNodes: []*types.ModelMLNodes{{MlNodes: []*types.MLNodeInfo{{NodeId: "A"}, {NodeId: "B"}}}},
		}},
	}))

	settled := func(id uint64, node string, created, closed int64) types.Trainshard {
		return types.Trainshard{
			TrainshardId:    id,
			Creator:         host,
			Status:          types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED,
			CreatedAtHeight: created,
			ClosedAtHeight:  closed,
			Nodes:           []*types.TrainshardReservedNode{{Participant: host, NodeId: node, ModelId: "model1"}},
		}
	}

	// staggered: A [100,140], B [150,190]
	require.NoError(t, k.Trainshards.Set(ctx, 1, settled(1, "A", 100, 140)))
	require.NoError(t, k.Trainshards.Set(ctx, 2, settled(2, "B", 150, 190)))
	view := k.BuildEpochReservationView(ctx, epoch)
	require.False(t, view.FullyReservedAt(host, 130))
	require.False(t, view.FullyReservedAt(host, 170))

	// overlap B with A [120,160]: both reserved only in [120,140]
	require.NoError(t, k.Trainshards.Set(ctx, 2, settled(2, "B", 120, 160)))
	view = k.BuildEpochReservationView(ctx, epoch)
	require.False(t, view.FullyReservedAt(host, 110)) // only A reserved
	require.True(t, view.FullyReservedAt(host, 130))  // both reserved -> shielded here
	require.False(t, view.FullyReservedAt(host, 150)) // only B reserved
}

// TestCollectEpochFullyReservedHostsForModel keeps hosts ever fully reserved
func TestCollectEpochFullyReservedHostsForModel(t *testing.T) {
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
			MlNodes: []*types.ModelMLNodes{{MlNodes: []*types.MLNodeInfo{{NodeId: "n1"}, {NodeId: "n2"}}}},
		}},
	}))

	// staggered: n1 then n2, never both
	require.NoError(t, k.Trainshards.Set(ctx, 1, types.Trainshard{
		TrainshardId: 1, Status: types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED,
		CreatedAtHeight: 110, ClosedAtHeight: 120,
		Nodes: []*types.TrainshardReservedNode{{Participant: host, NodeId: "n1", ModelId: "model1"}},
	}))
	require.NoError(t, k.Trainshards.Set(ctx, 2, types.Trainshard{
		TrainshardId: 2, Status: types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED,
		CreatedAtHeight: 130, ClosedAtHeight: 140,
		Nodes: []*types.TrainshardReservedNode{{Participant: host, NodeId: "n2", ModelId: "model1"}},
	}))
	require.Empty(t, k.CollectEpochFullyReservedHostsForModel(ctx, epoch, "model1"))

	// concurrent: both nodes reserved [110,140]
	require.NoError(t, k.Trainshards.Set(ctx, 2, types.Trainshard{
		TrainshardId: 2, Status: types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED,
		CreatedAtHeight: 110, ClosedAtHeight: 140,
		Nodes: []*types.TrainshardReservedNode{{Participant: host, NodeId: "n2", ModelId: "model1"}},
	}))
	require.Contains(t, k.CollectEpochFullyReservedHostsForModel(ctx, epoch, "model1"), host)
}

func TestCollectEpochReservedWeightTotals(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	const epoch = uint64(7)
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: epoch, PocStartBlockHeight: 100}))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: epoch + 1, PocStartBlockHeight: 200}))

	hostA := sample.AccAddress()
	hostB := sample.AccAddress()

	require.NoError(t, k.Trainshards.Set(ctx, 1, types.Trainshard{
		TrainshardId:    1,
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED,
		CreatedAtHeight: 100,
		ClosedAtHeight:  150,
		Nodes: []*types.TrainshardReservedNode{
			{Participant: hostA, NodeId: "A1", ModelId: "model1", PocWeight: 30},
			{Participant: hostA, NodeId: "A2", ModelId: "model2", PocWeight: 20},
			// A2 serves model2 and model3: host total still dedups by node
			{Participant: hostA, NodeId: "A2", ModelId: "model3", PocWeight: 20},
			{Participant: hostB, NodeId: "B1", ModelId: "model1", PocWeight: 40},
		},
	}))
	// same (model,node) in overlap: max wins
	require.NoError(t, k.Trainshards.Set(ctx, 2, types.Trainshard{
		TrainshardId:    2,
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED,
		CreatedAtHeight: 160,
		ClosedAtHeight:  190,
		Nodes: []*types.TrainshardReservedNode{
			{Participant: hostA, NodeId: "A1", ModelId: "model1", PocWeight: 50},
		},
	}))

	byModelHost, byHost := k.CollectEpochReservedWeightTotals(ctx, epoch)

	require.Equal(t, int64(50), byModelHost["model1"][hostA])
	require.Equal(t, int64(20), byModelHost["model2"][hostA])
	require.Equal(t, int64(20), byModelHost["model3"][hostA])
	require.Equal(t, int64(40), byModelHost["model1"][hostB])
	// A1 (max 50) + A2 (20, counted once), not 50+20+20
	require.Equal(t, int64(70), byHost[hostA])
	require.Equal(t, int64(40), byHost[hostB])
}

// TestTrainshardLifecycle_E2E walks the full lifecycle a creator drives:
// reserve -> reward-weight accounting -> PoC/inference shielding -> unfreeze -> expire.
func TestTrainshardLifecycle_E2E(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)
	const epoch = uint64(7)

	// reserve: assemble opens an ACTIVE shard and reserves one node (buffer keeps one free)
	resp, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.NoError(t, err)
	shard, err := k.Trainshards.Get(ctx, resp.TrainshardId)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE, shard.Status)
	require.Len(t, shard.Nodes, 1)
	reserved := shard.Nodes[0].NodeId
	free := "node-a"
	if reserved == "node-a" {
		free = "node-b"
	}
	require.True(t, k.IsNodeReserved(ctx, creator, reserved))
	require.False(t, k.IsNodeReserved(ctx, creator, free))

	// reward accounting: the reserved weight is what settlement and claim_rewards subtract
	byModelHost, byHost := k.CollectEpochReservedWeightTotals(ctx, epoch)
	require.Equal(t, int64(100), byHost[creator])
	require.Equal(t, int64(100), byModelHost["model1"][creator])

	// shielding: a partially reserved host is not fully reserved, so PoC/inference are untouched
	require.NotContains(t, k.CollectEpochFullyReservedHostsForModel(ctx, epoch, "model1"), creator)

	// unfreeze: settle releases the reservation
	_, err = ms.SettleTrainshard(ctx, &types.MsgSettleTrainshard{Creator: creator, TrainshardId: resp.TrainshardId})
	require.NoError(t, err)
	settled, err := k.Trainshards.Get(ctx, resp.TrainshardId)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED, settled.Status)
	require.False(t, k.IsNodeReserved(ctx, creator, reserved))

	// expire: a fresh ACTIVE shard is expired by the end-blocker at its expiry height
	require.NoError(t, k.TrainshardProposals.Set(ctx, 2, types.TrainshardProposal{
		Creator:           creator,
		GpuProfileId:      trainshardTestProfile,
		MaxNodes:          1,
		MaxDurationBlocks: 100,
		Id:                2,
		Status:            types.TrainshardProposalStatus_TRAINSHARD_PROPOSAL_STATUS_OPEN,
	}))
	resp2, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 2})
	require.NoError(t, err)
	active, err := k.Trainshards.Get(ctx, resp2.TrainshardId)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE, active.Status)

	k.ProcessTrainshardEndBlock(ctx.WithBlockHeight(active.ExpiresAtHeight))

	expired, err := k.Trainshards.Get(ctx, resp2.TrainshardId)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardStatus_TRAINSHARD_STATUS_EXPIRED, expired.Status)
	require.False(t, k.IsNodeReserved(ctx, creator, active.Nodes[0].NodeId))
}

// TestTrainshardFullReservation_ShieldsPocAndUnfreezes verifies a host whose only
// node for a model is reserved is fully shielded (no inference routing, PoC duties
// skipped, reward weight frozen) and is released cleanly on settle.
func TestTrainshardFullReservation_ShieldsPocAndUnfreezes(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	ms := keeper.NewMsgServerImpl(k)
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), gomock.Any()).Return(true).AnyTimes()

	const epoch = uint64(7)
	params := types.DefaultParams()
	params.TrainingParams.TrainingEnabled = true
	params.TrainingParams.MaxReservedSharePerModelBps = 10000
	params.TrainingParams.MaxReservedSharePerProfileBps = 10000
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epoch))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: epoch, PocStartBlockHeight: 1}))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: epoch + 1, PocStartBlockHeight: 1000}))

	hostA := sample.AccAddress() // reserved host: single node for model1
	hostB := sample.AccAddress() // free host: keeps the model capacity buffer satisfied
	single := func(node string) []*types.ModelMLNodes {
		return []*types.ModelMLNodes{{MlNodes: []*types.MLNodeInfo{{NodeId: node, PocWeight: 100}}}}
	}
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: epoch,
		Participants: []*types.ActiveParticipant{
			{Index: hostA, Models: []string{"model1"}, MlNodes: single("node-a")},
			{Index: hostB, Models: []string{"model1"}, MlNodes: single("node-b")},
		},
	}))
	hw := func(host, node string) *types.HardwareNodes {
		return &types.HardwareNodes{Participant: host, HardwareNodes: []*types.HardwareNode{
			{LocalId: node, Hardware: []*types.Hardware{{Type: "NVIDIA H100", Count: 8}}},
		}}
	}
	require.NoError(t, k.SetHardwareNodes(ctx, hw(hostA, "node-a")))
	require.NoError(t, k.SetHardwareNodes(ctx, hw(hostB, "node-b")))
	require.NoError(t, k.TrainingNodeOptIns.Set(ctx, collections.Join(hostA, "node-a")))
	require.NoError(t, k.TrainshardProposals.Set(ctx, 1, types.TrainshardProposal{
		Creator: hostA, GpuProfileId: trainshardTestProfile, MaxNodes: 1, MaxDurationBlocks: 100, Id: 1,
		Status: types.TrainshardProposalStatus_TRAINSHARD_PROPOSAL_STATUS_OPEN,
	}))

	ctx = ctx.WithBlockHeight(50)
	resp, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: hostA, ProposalId: 1})
	require.NoError(t, err)
	shard, err := k.Trainshards.Get(ctx, resp.TrainshardId)
	require.NoError(t, err)
	require.Len(t, shard.Nodes, 1)
	require.Equal(t, "node-a", shard.Nodes[0].NodeId)

	// inference: hostA has no free node left -> excluded from routing; hostB untouched
	require.True(t, k.IsNodeReserved(ctx, hostA, "node-a"))
	require.False(t, k.IsNodeReserved(ctx, hostB, "node-b"))

	// PoC duties: hostA fully reserved across the epoch (the predicate claim_rewards,
	// confirmation_poc and devshard escrow use to skip its validation/penalties)
	fully := k.CollectEpochFullyReservedHostsForModel(ctx, epoch, "model1")
	require.Contains(t, fully, hostA)
	require.NotContains(t, fully, hostB)
	view := k.BuildEpochReservationView(ctx, epoch)
	require.True(t, view.FullyReservedAt(hostA, 60))
	require.False(t, view.FullyReservedAt(hostB, 60))

	// reward weight frozen for hostA only
	_, byHost := k.CollectEpochReservedWeightTotals(ctx, epoch)
	require.Equal(t, int64(100), byHost[hostA])
	require.Zero(t, byHost[hostB])

	// unfreeze: settle clears the active reservation so routing/PoC are restored
	_, err = ms.SettleTrainshard(ctx, &types.MsgSettleTrainshard{Creator: hostA, TrainshardId: resp.TrainshardId})
	require.NoError(t, err)
	require.False(t, k.IsNodeReserved(ctx, hostA, "node-a"))
	require.Empty(t, k.CollectReservedNodeIds(ctx))
}

func TestSettleTrainshard_WrongCreatorRejected(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)

	_, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.NoError(t, err)

	_, err = ms.SettleTrainshard(ctx, &types.MsgSettleTrainshard{Creator: sample.AccAddress(), TrainshardId: 1})
	require.ErrorIs(t, err, types.ErrTrainshardNotCreator)

	shard, err := k.Trainshards.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE, shard.Status)
}
