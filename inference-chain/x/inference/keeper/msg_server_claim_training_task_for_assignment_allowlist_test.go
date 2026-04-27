package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestClaimTrainingTaskForAssignment_AllowListEnforced(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	ms := keeper.NewMsgServerImpl(k)
	wctx := sdk.UnwrapSDKContext(ctx)

	creator := "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2"

	// create a task
	sdkCtx := sdk.UnwrapSDKContext(wctx)
	participant := types.Participant{Index: creator, Address: creator}
	k.SetParticipant(ctx, participant)
	require.NoError(t, k.CreateTask(sdkCtx, &types.TrainingTask{
		Id:          0,
		RequestedBy: creator,
		Assigner:    creator,
		Config: &types.TrainingConfig{
			Datasets: &types.TrainingDatasets{
				Train: "train_data",
				Test:  "test_data",
			},
		},
	}))
	// next id allocated by CreateTask/GetNextTaskID; get top id 1

	// not allowed
	_, err := ms.ClaimTrainingTaskForAssignment(wctx, &types.MsgClaimTrainingTaskForAssignment{
		Creator: creator,
		TaskId:  1,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrTrainingNotAllowed)

	// allow
	acc, e := sdk.AccAddressFromBech32(creator)
	require.NoError(t, e)
	require.NoError(t, k.TrainingStartAllowListSet.Set(wctx, acc))

	// should succeed now
	// Advance block height to allow claiming (override assignment deadline)
	wctx = wctx.WithBlockHeight(200)
	_, err = ms.ClaimTrainingTaskForAssignment(wctx, &types.MsgClaimTrainingTaskForAssignment{
		Creator: creator,
		TaskId:  1,
	})
	require.NoError(t, err)
}
