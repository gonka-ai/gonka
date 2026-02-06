package keeper_test

import (
	"fmt"
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

		// Verify actual coin amounts per epoch: 1000/100 = 10 per epoch, no remainder
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

	t.Run("uneven division with remainder", func(t *testing.T) {
		k, ctx, mocks := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		// 1003 tokens over 100 epochs: 10 per epoch + 3 remainder in first epoch
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

		// First epoch gets base amount (10) + remainder (3) = 13
		expectedFirstEpoch := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(13)))
		require.True(t, schedule.EpochAmounts[0].Coins.Equal(expectedFirstEpoch),
			"epoch 0: expected %s, got %s", expectedFirstEpoch, schedule.EpochAmounts[0].Coins)

		// Remaining epochs get base amount only (10)
		expectedPerEpoch := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(10)))
		for i := 1; i < 100; i++ {
			require.True(t, schedule.EpochAmounts[i].Coins.Equal(expectedPerEpoch),
				"epoch %d: expected %s, got %s", i, expectedPerEpoch, schedule.EpochAmounts[i].Coins)
		}

		// Verify total across all epochs equals original amount
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
			VestingEpochs: keeper.MaxVestingEpochs,
		})
		require.NoError(t, err)

		schedule, found := k.GetVestingSchedule(wctx, recipient.String())
		require.True(t, found)
		require.Len(t, schedule.EpochAmounts, int(keeper.MaxVestingEpochs))

		// 3650 / 3650 = 1 per epoch, no remainder
		expectedPerEpoch := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1)))
		for i := 0; i < int(keeper.MaxVestingEpochs); i++ {
			require.True(t, schedule.EpochAmounts[i].Coins.Equal(expectedPerEpoch),
				"epoch %d: expected %s, got %s", i, expectedPerEpoch, schedule.EpochAmounts[i].Coins)
		}
	})

	t.Run("exceeds max vesting epochs", func(t *testing.T) {
		k, ctx, _ := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		_, err := ms.TransferWithVesting(wctx, &types.MsgTransferWithVesting{
			Sender:        sender.String(),
			Recipient:     recipient.String(),
			Amount:        sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1000))),
			VestingEpochs: keeper.MaxVestingEpochs + 1,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "exceeds maximum allowed")
	})

	t.Run("exceeds max coins", func(t *testing.T) {
		k, ctx, _ := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		// Create more than MaxCoinsInAmount denominations
		coins := sdk.NewCoins()
		for i := 0; i <= keeper.MaxCoinsInAmount; i++ {
			coins = coins.Add(sdk.NewCoin(fmt.Sprintf("denom%d", i), math.NewInt(100)))
		}

		_, err := ms.TransferWithVesting(wctx, &types.MsgTransferWithVesting{
			Sender:        sender.String(),
			Recipient:     recipient.String(),
			Amount:        coins,
			VestingEpochs: 180,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "too many coin denominations")
	})
}
