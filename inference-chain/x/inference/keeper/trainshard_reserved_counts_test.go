package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func setupTrainshardReturnBuffer(t *testing.T, tune func(*types.TrainingParams)) (keeper.Keeper, types.MsgServer, sdk.Context, string, *types.TrainshardReservedNode) {
	t.Helper()
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	tune(params.TrainingParams)
	require.NoError(t, k.SetParams(ctx, params))

	resp, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.NoError(t, err)
	_, err = ms.SettleTrainshard(ctx, &types.MsgSettleTrainshard{Creator: creator, TrainshardId: resp.TrainshardId})
	require.NoError(t, err)
	settled, err := k.Trainshards.Get(ctx, resp.TrainshardId)
	require.NoError(t, err)
	require.True(t, k.IsNodeReserved(ctx, creator, settled.Nodes[0].NodeId))

	require.NoError(t, k.TrainshardProposals.Set(ctx, 2, types.TrainshardProposal{
		Creator:           creator,
		GpuProfileId:      trainshardTestProfile,
		NodeCount:         1,
		MaxDurationBlocks: 100,
		Id:                2,
		Status:            types.TrainshardProposalStatus_TRAINSHARD_PROPOSAL_STATUS_OPEN,
	}))
	return k, ms, ctx, creator, settled.Nodes[0]
}

func TestAssembleTrainshard_ReturnBufferCountsAgainstCaps(t *testing.T) {
	k, ms, ctx, creator, buffered := setupTrainshardReturnBuffer(t, func(p *types.TrainingParams) {
		p.MaxReservedSharePerModelBps = 10000
		p.MaxReservedSharePerProfileBps = 10000
	})

	_, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 2})
	require.ErrorIs(t, err, types.ErrTrainshardCapacity)
	require.ErrorContains(t, err, "1 already reserved")
	require.ErrorContains(t, err, "1 over a model share cap or its last free node")

	k.ProcessTrainshardEndBlock(ctx.WithBlockHeight(buffered.ReservedUntilHeight))
	require.False(t, k.IsNodeReserved(ctx, creator, buffered.NodeId))

	resp, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 2})
	require.NoError(t, err)
	shard, err := k.Trainshards.Get(ctx, resp.TrainshardId)
	require.NoError(t, err)
	require.Len(t, shard.Nodes, 1)
}

func TestAssembleTrainshard_ReturnBufferCountsAgainstTheTotalCap(t *testing.T) {
	_, ms, ctx, creator, _ := setupTrainshardReturnBuffer(t, func(p *types.TrainingParams) {
		p.MaxReservedSharePerModelBps = 10000
		p.MaxReservedSharePerProfileBps = 10000
		p.MaxTotalReservedNodes = 1
	})

	_, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 2})
	require.ErrorIs(t, err, types.ErrTrainshardCapacity)
	require.ErrorContains(t, err, "1 over the total reserve cap (1)")
}

func TestAssembleTrainshard_ReturnBufferCountsAgainstTheProfileCap(t *testing.T) {
	_, ms, ctx, creator, _ := setupTrainshardReturnBuffer(t, func(p *types.TrainingParams) {
		p.MaxReservedSharePerModelBps = 10000
		p.MaxReservedSharePerProfileBps = 5000
	})

	_, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 2})
	require.ErrorIs(t, err, types.ErrTrainshardCapacity)
	require.ErrorContains(t, err, "1 over the profile share cap (1)")
}

func TestAssembleTrainshard_FailsOnReservationTheShardDoesNotHold(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)
	require.NoError(t, k.Trainshards.Set(ctx, 99, types.Trainshard{
		TrainshardId: 99,
		GpuProfileId: trainshardTestProfile,
		Status:       types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED,
		Nodes: []*types.TrainshardReservedNode{{Participant: creator, NodeId: "node-other", ModelId: "model1",
			Status: types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_RELEASED_ON_CLOSE}},
	}))
	require.NoError(t, k.TrainshardReservations.Set(ctx, collections.Join(creator, "node-gone"), uint64(99)))

	_, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.ErrorContains(t, err, "trainshard 99 holds no entry for reserved node node-gone")
}

func TestAssembleTrainshard_FailsOnReservationOfAMissingShard(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)
	require.NoError(t, k.TrainshardReservations.Set(ctx, collections.Join(creator, "node-gone"), uint64(99)))

	_, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.ErrorContains(t, err, "trainshard 99 reserving node node-gone")
	require.False(t, k.IsNodeReserved(ctx, creator, "node-a"))
	require.False(t, k.IsNodeReserved(ctx, creator, "node-b"))
}
