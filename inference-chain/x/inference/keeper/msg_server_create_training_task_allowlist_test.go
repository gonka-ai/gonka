package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestCreateTrainingTask_AllowListEnforced(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	ms := keeper.NewMsgServerImpl(k)
	wctx := sdk.UnwrapSDKContext(ctx)

	creator := "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2"
	// Register participant
	participant := types.Participant{Index: creator, Address: creator}
	k.SetParticipant(ctx, participant)

	config := &types.TrainingConfig{
		Datasets: &types.TrainingDatasets{
			Train: "train_data",
			Test:  "test_data",
		},
	}
	// not on allow list -> expect ErrTrainingNotAllowed
	_, err := ms.CreateTrainingTask(wctx, &types.MsgCreateTrainingTask{
		Creator:           creator,
		HardwareResources: []*types.TrainingHardwareResources{},
		Config:            config,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrTrainingNotAllowed)

	// add to allow list
	acc, e := sdk.AccAddressFromBech32(creator)
	require.NoError(t, e)
	require.NoError(t, k.TrainingStartAllowListSet.Set(wctx, acc))

	// now allowed -> should succeed
	resp, err := ms.CreateTrainingTask(wctx, &types.MsgCreateTrainingTask{
		Creator:           creator,
		HardwareResources: []*types.TrainingHardwareResources{},
		Config:            config,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
}
