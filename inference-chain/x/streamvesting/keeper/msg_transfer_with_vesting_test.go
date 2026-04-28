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
	sender := keepertest.StreamVestingGovAuthority()
	recipient := sdk.AccAddress("recipient_address___")

	t.Run("unauthorized sender", func(t *testing.T) {
		k, ctx, _ := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		unauthorized := sdk.AccAddress("random_sender_______")
		_, err := ms.TransferWithVesting(wctx, &types.MsgTransferWithVesting{
			Sender:        unauthorized.String(),
			Recipient:     recipient.String(),
			Amount:        sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1000))),
			VestingEpochs: 180,
		})
		require.Error(t, err)
		require.ErrorIs(t, err, types.ErrUnauthorizedSender)
	})

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

	t.Run("valid transfer with custom epochs", func(t *testing.T) {
		k, ctx, mocks := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		amount := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1000)))

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

		schedule, found := k.GetVestingSchedule(wctx, recipient.String())
		require.True(t, found)
		require.Equal(t, recipient.String(), schedule.ParticipantAddress)
		require.Len(t, schedule.EpochAmounts, 100)

		expectedPerEpoch := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(10)))
		for i := 0; i < 100; i++ {
			require.True(t, schedule.EpochAmounts[i].Coins.Equal(expectedPerEpoch),
				"epoch %d: expected %s, got %s", i, expectedPerEpoch, schedule.EpochAmounts[i].Coins)
		}
	})

	t.Run("valid transfer with default epochs", func(t *testing.T) {
		k, ctx, mocks := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		amount := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1800)))

		mocks.BankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), sender, types.ModuleName, amount, "transfer with vesting").
			Return(nil)
		mocks.BankKeeper.EXPECT().
			LogSubAccountTransaction(gomock.Any(), types.ModuleName, recipient.String(), keeper.HoldingSubAccount, gomock.Any(), gomock.Any())

		_, err := ms.TransferWithVesting(wctx, &types.MsgTransferWithVesting{
			Sender:        sender.String(),
			Recipient:     recipient.String(),
			Amount:        amount,
			VestingEpochs: 0,
		})
		require.NoError(t, err)

		schedule, found := k.GetVestingSchedule(wctx, recipient.String())
		require.True(t, found)
		require.Len(t, schedule.EpochAmounts, int(types.DefaultVestingEpochs))
	})

	t.Run("uneven division with remainder", func(t *testing.T) {
		k, ctx, mocks := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		amount := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1003)))

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

		schedule, found := k.GetVestingSchedule(wctx, recipient.String())
		require.True(t, found)
		require.Len(t, schedule.EpochAmounts, 100)

		expectedFirstEpoch := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(13)))
		require.True(t, schedule.EpochAmounts[0].Coins.Equal(expectedFirstEpoch),
			"epoch 0: expected %s, got %s", expectedFirstEpoch, schedule.EpochAmounts[0].Coins)

		expectedPerEpoch := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(10)))
		for i := 1; i < 100; i++ {
			require.True(t, schedule.EpochAmounts[i].Coins.Equal(expectedPerEpoch),
				"epoch %d: expected %s, got %s", i, expectedPerEpoch, schedule.EpochAmounts[i].Coins)
		}

		total := math.ZeroInt()
		for i := 0; i < 100; i++ {
			total = total.Add(schedule.EpochAmounts[i].Coins.AmountOf("stake"))
		}
		require.Equal(t, math.NewInt(1003), total, "total across epochs should equal original amount")
	})

	t.Run("max vesting epochs", func(t *testing.T) {
		k, ctx, mocks := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		amount := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(3650)))

		mocks.BankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), sender, types.ModuleName, amount, "transfer with vesting").
			Return(nil)
		mocks.BankKeeper.EXPECT().
			LogSubAccountTransaction(gomock.Any(), types.ModuleName, recipient.String(), keeper.HoldingSubAccount, gomock.Any(), gomock.Any())

		_, err := ms.TransferWithVesting(wctx, &types.MsgTransferWithVesting{
			Sender:        sender.String(),
			Recipient:     recipient.String(),
			Amount:        amount,
			VestingEpochs: types.MaxVestingEpochs,
		})
		require.NoError(t, err)

		schedule, found := k.GetVestingSchedule(wctx, recipient.String())
		require.True(t, found)
		require.Len(t, schedule.EpochAmounts, int(types.MaxVestingEpochs))

		expectedPerEpoch := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1)))
		for i := 0; i < int(types.MaxVestingEpochs); i++ {
			require.True(t, schedule.EpochAmounts[i].Coins.Equal(expectedPerEpoch),
				"epoch %d: expected %s, got %s", i, expectedPerEpoch, schedule.EpochAmounts[i].Coins)
		}
	})

	t.Run("inference module as sender", func(t *testing.T) {
		k, ctx, mocks := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		infSender := keepertest.StreamVestingInferenceAuthority()
		amount := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(500)))

		mocks.BankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), infSender, types.ModuleName, amount, "transfer with vesting").
			Return(nil)
		mocks.BankKeeper.EXPECT().
			LogSubAccountTransaction(gomock.Any(), types.ModuleName, recipient.String(), keeper.HoldingSubAccount, gomock.Any(), gomock.Any())

		_, err := ms.TransferWithVesting(wctx, &types.MsgTransferWithVesting{
			Sender:        infSender.String(),
			Recipient:     recipient.String(),
			Amount:        amount,
			VestingEpochs: 10,
		})
		require.NoError(t, err)

		schedule, found := k.GetVestingSchedule(wctx, recipient.String())
		require.True(t, found)
		require.Len(t, schedule.EpochAmounts, 10)
	})
}
