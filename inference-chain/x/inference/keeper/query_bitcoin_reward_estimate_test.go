package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/types"
)

func TestEstimateBitcoinReward_InvalidRequest(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	_, err := k.EstimateBitcoinReward(ctx, nil)
	require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "invalid request"))

	_, err = k.EstimateBitcoinReward(ctx, &types.QueryEstimateBitcoinRewardRequest{
		EpochIndex:  0,
		Participant: sample.AccAddress(),
	})
	require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "epoch_index must be positive"))

	_, err = k.EstimateBitcoinReward(ctx, &types.QueryEstimateBitcoinRewardRequest{
		EpochIndex:  1,
		Participant: "invalid",
	})
	require.ErrorIs(t, err, status.Error(codes.InvalidArgument, "invalid participant address"))
}

func TestEstimateBitcoinReward_ActiveParticipantsNotFound(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	_, err := k.EstimateBitcoinReward(ctx, &types.QueryEstimateBitcoinRewardRequest{
		EpochIndex:  12,
		Participant: sample.AccAddress(),
	})

	require.ErrorIs(t, err, status.Error(codes.NotFound, "active participants not found for epoch"))
}
