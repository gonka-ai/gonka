package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/streamvesting/keeper"
	"github.com/productscience/inference/x/streamvesting/types"
)

func TestMsgTransferWithVesting(t *testing.T) {
	sender := sdk.AccAddress("sender_address______")
	recipient := sdk.AccAddress("recipient_address___")

	t.Run("invalid sender address", func(t *testing.T) {
		k, ctx, _ := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		_, err := ms.TransferWithVesting(wctx, &types.MsgTransferWithVesting{
			Sender:        "invalid",
			Recipient:     recipient.String(),
			Amount:        sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1000))),
			VestingEpochs: 180,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid sender address")
	})

	t.Run("invalid recipient address", func(t *testing.T) {
		k, ctx, _ := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		_, err := ms.TransferWithVesting(wctx, &types.MsgTransferWithVesting{
			Sender:        sender.String(),
			Recipient:     "invalid",
			Amount:        sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1000))),
			VestingEpochs: 180,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid recipient address")
	})

	t.Run("zero amount", func(t *testing.T) {
		k, ctx, _ := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		_, err := ms.TransferWithVesting(wctx, &types.MsgTransferWithVesting{
			Sender:        sender.String(),
			Recipient:     recipient.String(),
			Amount:        sdk.NewCoins(),
			VestingEpochs: 180,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "amount cannot be zero")
	})

	t.Run("valid transfer with custom epochs", func(t *testing.T) {
		k, ctx, mocks := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		amount := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1000)))

		// Set up mock expectations
		mocks.BankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), sender, types.ModuleName, amount, "transfer with vesting").
			Return(nil)
		mocks.BankKeeper.EXPECT().
			LogSubAccountTransaction(gomock.Any(), types.ModuleName, recipient.String(), keeper.HoldingSubAccount, gomock.Any(), gomock.Any())

		_, err := ms.TransferWithVesting(wctx, &types.MsgTransferWithVesting{
			Sender:        sender.String(),
			Recipient:     recipient.String(),
			Amount:        amount,
			VestingEpochs: 100,
		})
		require.NoError(t, err)

		// Verify vesting schedule was created
		schedule, found := k.GetVestingSchedule(wctx, recipient.String())
		require.True(t, found)
		require.Equal(t, recipient.String(), schedule.ParticipantAddress)
		require.Len(t, schedule.EpochAmounts, 100)
	})

	t.Run("valid transfer with default epochs", func(t *testing.T) {
		k, ctx, mocks := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		amount := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1800)))

		// Set up mock expectations
		mocks.BankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), sender, types.ModuleName, amount, "transfer with vesting").
			Return(nil)
		mocks.BankKeeper.EXPECT().
			LogSubAccountTransaction(gomock.Any(), types.ModuleName, recipient.String(), keeper.HoldingSubAccount, gomock.Any(), gomock.Any())

		_, err := ms.TransferWithVesting(wctx, &types.MsgTransferWithVesting{
			Sender:        sender.String(),
			Recipient:     recipient.String(),
			Amount:        amount,
			VestingEpochs: 0, // 0 means default 180
		})
		require.NoError(t, err)

		// Verify vesting schedule was created with default epochs
		schedule, found := k.GetVestingSchedule(wctx, recipient.String())
		require.True(t, found)
		require.Len(t, schedule.EpochAmounts, int(keeper.DefaultVestingEpochs))
	})
}
