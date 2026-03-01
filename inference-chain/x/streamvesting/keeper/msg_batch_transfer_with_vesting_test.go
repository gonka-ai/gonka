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

func TestMsgBatchTransferWithVesting(t *testing.T) {
	sender := testAddress(1)
	recipient1 := testAddress(2)
	recipient2 := testAddress(3)

	t.Run("empty outputs", func(t *testing.T) {
		k, ctx, _ := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		_, err := ms.BatchTransferWithVesting(wctx, &types.MsgBatchTransferWithVesting{
			Sender:        sender,
			Outputs:       nil,
			VestingEpochs: 100,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "outputs cannot be empty")
	})

	t.Run("invalid recipient address", func(t *testing.T) {
		k, ctx, _ := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		_, err := ms.BatchTransferWithVesting(wctx, &types.MsgBatchTransferWithVesting{
			Sender: sender,
			Outputs: []types.BatchVestingOutput{
				{
					Recipient: "invalid",
					Amount:    sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1))),
				},
			},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid recipient address")
	})

	t.Run("bank transfer failure", func(t *testing.T) {
		k, ctx, mocks := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)
		senderAddr, err := sdk.AccAddressFromBech32(sender)
		require.NoError(t, err)

		total := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(100)))
		mocks.BankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), senderAddr, types.ModuleName, total, "batch transfer with vesting").
			Return(fmt.Errorf("insufficient funds"))

		_, err = ms.BatchTransferWithVesting(wctx, &types.MsgBatchTransferWithVesting{
			Sender: sender,
			Outputs: []types.BatchVestingOutput{
				{
					Recipient: recipient1,
					Amount:    total,
				},
			},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to transfer coins from sender to module")
	})

	t.Run("happy path with duplicate recipients", func(t *testing.T) {
		k, ctx, mocks := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)
		senderAddr, err := sdk.AccAddressFromBech32(sender)
		require.NoError(t, err)

		total := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1800)))
		mocks.BankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), senderAddr, types.ModuleName, total, "batch transfer with vesting").
			Return(nil)
		mocks.BankKeeper.EXPECT().
			LogSubAccountTransaction(gomock.Any(), types.ModuleName, gomock.Any(), keeper.HoldingSubAccount, gomock.Any(), gomock.Any()).
			AnyTimes()

		_, err = ms.BatchTransferWithVesting(wctx, &types.MsgBatchTransferWithVesting{
			Sender: sender,
			Outputs: []types.BatchVestingOutput{
				{
					Recipient: recipient1,
					Amount:    sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1000))),
				},
				{
					Recipient: recipient1,
					Amount:    sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(500))),
				},
				{
					Recipient: recipient2,
					Amount:    sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(300))),
				},
			},
			VestingEpochs: 100,
		})
		require.NoError(t, err)

		schedule1, found := k.GetVestingSchedule(wctx, recipient1)
		require.True(t, found)
		require.Len(t, schedule1.EpochAmounts, 100)
		require.True(t, schedule1.EpochAmounts[0].Coins.Equal(sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(15)))))

		schedule2, found := k.GetVestingSchedule(wctx, recipient2)
		require.True(t, found)
		require.Len(t, schedule2.EpochAmounts, 100)
		require.True(t, schedule2.EpochAmounts[0].Coins.Equal(sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(3)))))
	})

	t.Run("happy path with unique recipients", func(t *testing.T) {
		k, ctx, mocks := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)
		senderAddr, err := sdk.AccAddressFromBech32(sender)
		require.NoError(t, err)

		total := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(900)))
		mocks.BankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), senderAddr, types.ModuleName, total, "batch transfer with vesting").
			Return(nil)
		mocks.BankKeeper.EXPECT().
			LogSubAccountTransaction(gomock.Any(), types.ModuleName, gomock.Any(), keeper.HoldingSubAccount, gomock.Any(), gomock.Any()).
			AnyTimes()

		_, err = ms.BatchTransferWithVesting(wctx, &types.MsgBatchTransferWithVesting{
			Sender: sender,
			Outputs: []types.BatchVestingOutput{
				{
					Recipient: recipient1,
					Amount:    sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(600))),
				},
				{
					Recipient: recipient2,
					Amount:    sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(300))),
				},
			},
			VestingEpochs: 3,
		})
		require.NoError(t, err)

		schedule1, found := k.GetVestingSchedule(wctx, recipient1)
		require.True(t, found)
		require.Len(t, schedule1.EpochAmounts, 3)
		require.True(t, schedule1.EpochAmounts[0].Coins.Equal(sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(200)))))

		schedule2, found := k.GetVestingSchedule(wctx, recipient2)
		require.True(t, found)
		require.Len(t, schedule2.EpochAmounts, 3)
		require.True(t, schedule2.EpochAmounts[0].Coins.Equal(sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(100)))))
	})

	t.Run("default vesting epochs when zero", func(t *testing.T) {
		k, ctx, mocks := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)
		senderAddr, err := sdk.AccAddressFromBech32(sender)
		require.NoError(t, err)

		total := sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1800)))
		mocks.BankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), senderAddr, types.ModuleName, total, "batch transfer with vesting").
			Return(nil)
		mocks.BankKeeper.EXPECT().
			LogSubAccountTransaction(gomock.Any(), types.ModuleName, gomock.Any(), keeper.HoldingSubAccount, gomock.Any(), gomock.Any()).
			AnyTimes()

		_, err = ms.BatchTransferWithVesting(wctx, &types.MsgBatchTransferWithVesting{
			Sender: sender,
			Outputs: []types.BatchVestingOutput{
				{
					Recipient: recipient1,
					Amount:    sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1800))),
				},
			},
			VestingEpochs: 0, // default should be applied
		})
		require.NoError(t, err)

		schedule, found := k.GetVestingSchedule(wctx, recipient1)
		require.True(t, found)
		require.Len(t, schedule.EpochAmounts, int(keeper.DefaultVestingEpochs))
		require.True(t, schedule.EpochAmounts[0].Coins.Equal(sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(10)))))
	})

	t.Run("too many total coin entries", func(t *testing.T) {
		k, ctx, _ := keepertest.StreamVestingKeeperWithMocks(t)
		ms := keeper.NewMsgServerImpl(k)
		wctx := sdk.UnwrapSDKContext(ctx)

		outputs := make([]types.BatchVestingOutput, 0, keeper.MaxBatchCoinEntries/keeper.MaxCoinsInAmount+1)
		for i := 0; i < keeper.MaxBatchCoinEntries/keeper.MaxCoinsInAmount+1; i++ {
			amount := sdk.NewCoins()
			for d := 0; d < keeper.MaxCoinsInAmount; d++ {
				amount = amount.Add(sdk.NewCoin(fmt.Sprintf("denom%d", d), math.NewInt(1)))
			}

			outputs = append(outputs, types.BatchVestingOutput{
				Recipient: testAddress(byte(10 + i%200)),
				Amount:    amount,
			})
		}

		_, err := ms.BatchTransferWithVesting(wctx, &types.MsgBatchTransferWithVesting{
			Sender:  sender,
			Outputs: outputs,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "too many total coin entries in batch")
	})
}

func testAddress(seed byte) string {
	b := make([]byte, 20)
	for i := range b {
		b[i] = seed
	}
	return sdk.AccAddress(b).String()
}
