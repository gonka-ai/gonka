package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/types"
)

func TestEstimateBitcoinReward_InvalidRequest(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	_, err := k.EstimateBitcoinReward(ctx, nil)
	require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "invalid request"))

	_, err = k.EstimateBitcoinReward(ctx, &types.QueryEstimateBitcoinRewardRequest{
		Participant: "invalid",
	})
	require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "invalid participant address"))
}

func TestEstimateBitcoinReward_SnapshotNotFound(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	_, err := k.EstimateBitcoinReward(ctx, &types.QueryEstimateBitcoinRewardRequest{
		Participant: sample.AccAddress(),
	})

	require.ErrorIs(t, err, status.Error(codes.NotFound, "delegation reward snapshot not found"))
}

func TestEstimateBitcoinReward_ActiveParticipantsNotFound(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetDelegationRewardTransferSnapshot(ctx, types.DelegationRewardTransferSnapshot{
		EpochIndex: 12,
	}))

	_, err := k.EstimateBitcoinReward(ctx, &types.QueryEstimateBitcoinRewardRequest{
		Participant: sample.AccAddress(),
	})

	require.ErrorIs(t, err, status.Error(codes.NotFound, "active participants not found for epoch"))
}

func TestEstimateBitcoinReward_AppliesSnapshotPenalty(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	const epoch = uint64(5)
	addr1 := testutil.Executor
	addr2 := testutil.Executor2

	for _, addr := range []string{addr1, addr2} {
		require.NoError(t, k.SetParticipant(ctx, types.Participant{
			Index:             addr,
			Address:           addr,
			Status:            types.ParticipantStatus_ACTIVE,
			CurrentEpochStats: &types.CurrentEpochStats{InferenceCount: 100, MissedRequests: 0},
		}))
	}

	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: epoch,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: addr1, Weight: 1000, ConfirmationWeight: 1000},
			{MemberAddress: addr2, Weight: 1000, ConfirmationWeight: 1000},
		},
	})
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId: epoch,
		Participants: []*types.ActiveParticipant{
			{Index: addr1},
			{Index: addr2},
		},
	}))
	_, err := k.EstimateBitcoinReward(ctx, &types.QueryEstimateBitcoinRewardRequest{Participant: addr1})
	require.ErrorIs(t, err, status.Error(codes.NotFound, "delegation reward snapshot not found"))

	require.NoError(t, k.SetDelegationRewardTransferSnapshot(ctx, types.DelegationRewardTransferSnapshot{
		EpochIndex: epoch,
		Penalties: []*types.DelegationRewardPenalty{
			{Participant: addr1, PenaltyFraction: types.DecimalFromFloat(0.5)},
		},
	}))

	resp1, err := k.EstimateBitcoinReward(ctx, &types.QueryEstimateBitcoinRewardRequest{Participant: addr1})
	require.NoError(t, err)
	require.Equal(t, addr1, resp1.SettleAmount.Participant)
	require.Equal(t, epoch, resp1.SettleAmount.EpochIndex)

	resp2, err := k.EstimateBitcoinReward(ctx, &types.QueryEstimateBitcoinRewardRequest{Participant: addr2})
	require.NoError(t, err)

	require.Greater(t, resp2.SettleAmount.RewardCoins, uint64(0))
	require.Less(t, resp1.SettleAmount.RewardCoins, resp2.SettleAmount.RewardCoins)
}

func TestEstimateBitcoinReward_ProtectsOnlyTheLeasedPartOfTheRunningEpoch(t *testing.T) {
	const epoch = uint64(5)
	host := testutil.Executor
	other := testutil.Executor2
	estimate := func(t *testing.T, shard types.Trainshard) uint64 {
		k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
		require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: epoch, PocStartBlockHeight: 100}))
		stats := map[string]*types.CurrentEpochStats{
			host:  {InferenceCount: 10, MissedRequests: 90},
			other: {InferenceCount: 100},
		}
		for _, addr := range []string{host, other} {
			require.NoError(t, k.SetParticipant(ctx, types.Participant{
				Index: addr, Address: addr, Status: types.ParticipantStatus_ACTIVE, CurrentEpochStats: stats[addr],
			}))
		}
		k.SetEpochGroupData(ctx, types.EpochGroupData{
			EpochIndex: epoch,
			ValidationWeights: []*types.ValidationWeight{
				{MemberAddress: host, Weight: 1000, ConfirmationWeight: 1000},
				{MemberAddress: other, Weight: 1000, ConfirmationWeight: 1000},
			},
		})
		k.SetEpochGroupData(ctx, types.EpochGroupData{
			EpochIndex: epoch,
			ModelId:    "model1",
			ValidationWeights: []*types.ValidationWeight{
				{MemberAddress: host, MlNodes: []*types.MLNodeInfo{{NodeId: "free", PocWeight: 600}, {NodeId: "resv", PocWeight: 400}}},
				{MemberAddress: other, MlNodes: []*types.MLNodeInfo{{NodeId: "other-node", PocWeight: 1000}}},
			},
		})
		require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
			EpochId:      epoch,
			Participants: []*types.ActiveParticipant{{Index: host}, {Index: other}},
		}))
		require.NoError(t, k.SetDelegationRewardTransferSnapshot(ctx, types.DelegationRewardTransferSnapshot{EpochIndex: epoch}))
		shard.Nodes = []*types.TrainshardReservedNode{{Participant: host, NodeId: "resv", ModelId: "model1", PocWeight: 400,
			Status: types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE}}
		if shard.ClosedAtHeight > 0 {
			shard.Nodes[0].Status = types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_RELEASED_ON_CLOSE
			shard.Nodes[0].ReleasedAtHeight = shard.ClosedAtHeight
			shard.Nodes[0].ReservedUntilHeight = shard.ClosedAtHeight + 20
		}
		require.NoError(t, k.Trainshards.Set(ctx, 1, shard))

		resp, err := k.EstimateBitcoinReward(ctx, &types.QueryEstimateBitcoinRewardRequest{Participant: host})
		require.NoError(t, err)
		return resp.SettleAmount.RewardCoins
	}

	whole := estimate(t, types.Trainshard{TrainshardId: 1, Status: types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE, CreatedAtHeight: 50})
	params := types.DefaultParams()
	quarterEnd := 100 + params.EpochParams.EpochLength/4 - 1
	quarter := estimate(t, types.Trainshard{TrainshardId: 1, Status: types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED, CreatedAtHeight: 100, ClosedAtHeight: quarterEnd})

	require.Greater(t, whole, uint64(0))
	require.InDelta(t, float64(whole)/4, float64(quarter), 1)
}
