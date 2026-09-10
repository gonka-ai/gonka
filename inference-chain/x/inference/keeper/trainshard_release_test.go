package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestAutokickTrainshardNode_ClosesShardWithoutActiveNodes(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)

	resp, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.NoError(t, err)
	shard, err := k.Trainshards.Get(ctx, resp.TrainshardId)
	require.NoError(t, err)
	node := shard.Nodes[0].NodeId

	kick := &types.MsgAutokickTrainshardNode{
		Creator:      creator,
		TrainshardId: resp.TrainshardId,
		Participant:  creator,
		NodeId:       node,
		Reason:       "nccl timeout",
		RequestId:    "req-1",
	}
	_, err = ms.AutokickTrainshardNode(ctx, kick)
	require.NoError(t, err)

	closed, err := k.Trainshards.Get(ctx, resp.TrainshardId)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardStatus_TRAINSHARD_STATUS_EXPIRED, closed.Status)
	require.Equal(t, types.TrainshardCloseReason_TRAINSHARD_CLOSE_REASON_NO_ACTIVE_NODES, closed.CloseReason)
	require.Equal(t, types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_AUTOKICKED, closed.Nodes[0].Status)
	require.Equal(t, "nccl timeout", closed.Nodes[0].ReleaseReason)

	require.True(t, k.IsNodeReserved(ctx, creator, node))
	require.False(t, k.HasActiveTrainReservation(ctx, creator))
	k.ProcessTrainshardEndBlock(ctx.WithBlockHeight(closed.Nodes[0].ReservedUntilHeight))
	require.False(t, k.IsNodeReserved(ctx, creator, node))
}

func TestAutokickTrainshardNode_IdempotentPerRequestId(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)

	resp, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.NoError(t, err)
	shard, err := k.Trainshards.Get(ctx, resp.TrainshardId)
	require.NoError(t, err)

	kick := &types.MsgAutokickTrainshardNode{
		Creator:      creator,
		TrainshardId: resp.TrainshardId,
		Participant:  creator,
		NodeId:       shard.Nodes[0].NodeId,
		RequestId:    "req-1",
	}
	_, err = ms.AutokickTrainshardNode(ctx, kick)
	require.NoError(t, err)
	first, err := k.Trainshards.Get(ctx, resp.TrainshardId)
	require.NoError(t, err)

	_, err = ms.AutokickTrainshardNode(ctx, kick)
	require.NoError(t, err)
	second, err := k.Trainshards.Get(ctx, resp.TrainshardId)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestAutokickTrainshardNode_RejectsForeignSigner(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)

	resp, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.NoError(t, err)
	shard, err := k.Trainshards.Get(ctx, resp.TrainshardId)
	require.NoError(t, err)

	_, err = ms.AutokickTrainshardNode(ctx, &types.MsgAutokickTrainshardNode{
		Creator:      sample.AccAddress(),
		TrainshardId: resp.TrainshardId,
		Participant:  creator,
		NodeId:       shard.Nodes[0].NodeId,
		RequestId:    "req-1",
	})
	require.ErrorIs(t, err, types.ErrTrainshardAutokickNotAllowed)

	unchanged, err := k.Trainshards.Get(ctx, resp.TrainshardId)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE, unchanged.Status)
}

func TestAutokickTrainshardNode_RejectsRequestIdReusedForAnotherNode(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)
	host := sample.AccAddress()

	require.NoError(t, k.Trainshards.Set(ctx, 9, types.Trainshard{
		TrainshardId:    9,
		Creator:         creator,
		GpuProfileId:    trainshardTestProfile,
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		CreatedAtHeight: ctx.BlockHeight(),
		ExpiresAtHeight: ctx.BlockHeight() + 100,
		Nodes: []*types.TrainshardReservedNode{
			{Participant: host, NodeId: "node-h1", ModelId: "model1", PocWeight: 100,
				Status: types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE},
			{Participant: host, NodeId: "node-h2", ModelId: "model1", PocWeight: 100,
				Status: types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE},
		},
	}))

	kick := func(nodeId string) error {
		_, err := ms.AutokickTrainshardNode(ctx, &types.MsgAutokickTrainshardNode{
			Creator:      host,
			TrainshardId: 9,
			Participant:  host,
			NodeId:       nodeId,
			RequestId:    "req-1",
		})
		return err
	}
	require.NoError(t, kick("node-h1"))
	require.ErrorIs(t, kick("node-h2"), types.ErrTrainshardAutokickRequestReused)
	require.NoError(t, kick("node-h1"))

	shard, err := k.Trainshards.Get(ctx, 9)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_AUTOKICKED, shard.Nodes[0].Status)
	require.Equal(t, types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE, shard.Nodes[1].Status)
}

func TestAutokickTrainshardNode_LegacyHandledKeyIsNoop(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)
	host := sample.AccAddress()

	require.NoError(t, k.Trainshards.Set(ctx, 9, types.Trainshard{
		TrainshardId:    9,
		Creator:         creator,
		GpuProfileId:    trainshardTestProfile,
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		CreatedAtHeight: ctx.BlockHeight(),
		ExpiresAtHeight: ctx.BlockHeight() + 100,
		Nodes: []*types.TrainshardReservedNode{
			{Participant: host, NodeId: "node-h", ModelId: "model1", PocWeight: 100,
				Status: types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE},
		},
	}))
	require.NoError(t, k.TrainshardAutokickRequest.Set(ctx, collections.Join(uint64(9), "legacy-req"), ""))

	_, err := ms.AutokickTrainshardNode(ctx, &types.MsgAutokickTrainshardNode{
		Creator:      host,
		TrainshardId: 9,
		Participant:  host,
		NodeId:       "node-h",
		RequestId:    "legacy-req",
	})
	require.NoError(t, err)

	shard, err := k.Trainshards.Get(ctx, 9)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE, shard.Status)
	require.Equal(t, types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE, shard.Nodes[0].Status)
}

func TestAutokickTrainshardNode_AllowsHostForOwnNode(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)
	host := sample.AccAddress()

	require.NoError(t, k.Trainshards.Set(ctx, 9, types.Trainshard{
		TrainshardId:    9,
		Creator:         creator,
		GpuProfileId:    trainshardTestProfile,
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		CreatedAtHeight: ctx.BlockHeight(),
		ExpiresAtHeight: ctx.BlockHeight() + 100,
		Nodes: []*types.TrainshardReservedNode{
			{Participant: host, NodeId: "node-h", ModelId: "model1", PocWeight: 100,
				Status: types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE},
		},
	}))

	_, err := ms.AutokickTrainshardNode(ctx, &types.MsgAutokickTrainshardNode{
		Creator:      host,
		TrainshardId: 9,
		Participant:  host,
		NodeId:       "node-h",
		Reason:       "gpu fell off the bus",
		RequestId:    "req-host",
	})
	require.NoError(t, err)

	shard, err := k.Trainshards.Get(ctx, 9)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_AUTOKICKED, shard.Nodes[0].Status)
}

func TestAssembleTrainshard_RejectsProfileDisallowedAfterVote(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)

	params := types.DefaultParams()
	params.TrainingParams.TrainingEnabled = true
	params.TrainingParams.AllowedGpuProfileIds = []string{"NVIDIA A100 x8"}
	require.NoError(t, k.SetParams(ctx, params))

	_, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.ErrorIs(t, err, types.ErrTrainshardProfileNotAllowed)
}

func TestReservationScopes_RewardWindowExcludesReturnBuffer(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	const epoch = uint64(7)
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: epoch, PocStartBlockHeight: 100}))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: epoch + 1, PocStartBlockHeight: 200}))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: epoch + 2, PocStartBlockHeight: 300}))

	host := sample.AccAddress()
	require.NoError(t, k.Trainshards.Set(ctx, 1, types.Trainshard{
		TrainshardId:    1,
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED,
		CreatedAtHeight: 100,
		ClosedAtHeight:  195,
		Nodes: []*types.TrainshardReservedNode{{
			Participant:         host,
			NodeId:              "node-a",
			ModelId:             "model1",
			PocWeight:           100,
			Status:              types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_RELEASED_ON_CLOSE,
			ReleasedAtHeight:    195,
			ReservedUntilHeight: 215,
		}},
	}))

	_, rewardNext := k.CollectEpochReservedWeightTotals(ctx, epoch+1, keeper.ReservationScopeReward)
	require.Zero(t, rewardNext[host])

	_, shieldNext := k.CollectEpochReservedWeightTotals(ctx, epoch+1, keeper.ReservationScopeShield)
	require.Equal(t, int64(100), shieldNext[host])

	_, rewardClosing := k.CollectEpochReservedWeightTotals(ctx, epoch, keeper.ReservationScopeReward)
	require.Equal(t, int64(100), rewardClosing[host])

	_, shieldInBuffer := k.CollectEpochReservedWeightTotalsAtHeight(ctx, epoch+1, 210, keeper.ReservationScopeShield)
	require.Equal(t, int64(100), shieldInBuffer[host])

	_, shieldAfterBuffer := k.CollectEpochReservedWeightTotalsAtHeight(ctx, epoch+1, 220, keeper.ReservationScopeShield)
	require.Zero(t, shieldAfterBuffer[host])
}

func TestAssembleTrainshard_IgnoresNodesWithoutId(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)
	setTrainshardEpochNodes(t, k, ctx, creator, []*types.MLNodeInfo{
		{NodeId: "node-a", PocWeight: 100},
		{NodeId: "", PocWeight: 100},
	})

	_, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.ErrorIs(t, err, types.ErrTrainshardCapacity)
	require.Empty(t, k.CollectReservedNodeIds(ctx))
}

func TestAssembleTrainshard_DedupsRepeatedNodeIdCapacity(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)
	setTrainshardEpochNodes(t, k, ctx, creator, []*types.MLNodeInfo{
		{NodeId: "node-a", PocWeight: 100},
		{NodeId: "node-a", PocWeight: 100},
	})

	_, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.ErrorIs(t, err, types.ErrTrainshardCapacity)
	require.Empty(t, k.CollectReservedNodeIds(ctx))
}

func setTrainshardEpochNodes(t *testing.T, k keeper.Keeper, ctx sdk.Context, creator string, model1Nodes []*types.MLNodeInfo) {
	t.Helper()
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: 7,
		Participants: []*types.ActiveParticipant{{
			Index:  creator,
			Models: []string{"model1", "model2"},
			MlNodes: []*types.ModelMLNodes{
				{MlNodes: model1Nodes},
				{MlNodes: []*types.MLNodeInfo{{NodeId: "node-b", PocWeight: 100}}},
			},
		}},
	}))
}

func TestRefreshTrainingNodeOptIn_MovesExpiryForward(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)
	ttl := types.DefaultParams().TrainingParams.OptInTtlBlocks

	resp, err := ms.RefreshTrainingNodeOptIn(ctx, &types.MsgRefreshTrainingNodeOptIn{
		Creator: creator,
		NodeIds: []string{"node-a"},
	})
	require.NoError(t, err)
	require.Equal(t, ctx.BlockHeight()+ttl, resp.ExpiresAtHeight)

	expiresAt, err := k.TrainingNodeOptIns.Get(ctx, collections.Join(creator, "node-a"))
	require.NoError(t, err)
	require.Equal(t, resp.ExpiresAtHeight, expiresAt)

	untouched, err := k.TrainingNodeOptIns.Get(ctx, collections.Join(creator, "node-b"))
	require.NoError(t, err)
	require.Equal(t, trainshardOptInExpiry, untouched)
}

func TestRefreshTrainingNodeOptIn_RejectsForeignNode(t *testing.T) {
	_, ms, ctx, creator := setupTrainshardFlow(t, 1)

	_, err := ms.RefreshTrainingNodeOptIn(ctx, &types.MsgRefreshTrainingNodeOptIn{
		Creator: creator,
		NodeIds: []string{"node-a", "node-x"},
	})
	require.ErrorIs(t, err, types.ErrTrainshardNodeNotOwned)
}

func TestAssembleTrainshard_SkipsExpiredOptIn(t *testing.T) {
	k, ms, ctx, creator := setupTrainshardFlow(t, 1)
	require.NoError(t, k.TrainingNodeOptIns.Set(ctx, collections.Join(creator, "node-a"), ctx.BlockHeight()))
	require.NoError(t, k.TrainingNodeOptIns.Set(ctx, collections.Join(creator, "node-b"), ctx.BlockHeight()))

	_, err := ms.AssembleTrainshard(ctx, &types.MsgAssembleTrainshard{Creator: creator, ProposalId: 1})
	require.ErrorIs(t, err, types.ErrTrainshardCapacity)
}
