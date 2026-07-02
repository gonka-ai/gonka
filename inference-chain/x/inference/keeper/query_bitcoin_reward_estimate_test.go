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
