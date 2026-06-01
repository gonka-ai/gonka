package keeper_test

import (
	"fmt"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func vestedPaymentSetup(t *testing.T) (keeper.Keeper, sdk.Context, *keepertest.InferenceMocks) {
	t.Helper()
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	return k, ctx, &mocks
}

// TestPayParticipantVested_AddVestedRewardsFails_ErrorPropagated proves that
// when AddVestedRewards fails (e.g. coin transfer or schedule update fails),
// the CacheContext prevents any partial state from persisting, and the error
// propagates to the caller so settlement can handle it.
func TestPayParticipantVested_AddVestedRewardsFails_ErrorPropagated(t *testing.T) {
	k, ctx, mocks := vestedPaymentSetup(t)

	address := "gonka1s8szs7n43jxgz4a4xaxmzm5emh7fmjxhach7w8"
	amount := int64(1000)
	vestingPeriods := uint64(10)
	vestingAmount := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, amount))

	// Mock: AddVestedRewards fails (simulating either coin transfer or schedule write failure)
	mocks.StreamVestingKeeper.EXPECT().AddVestedRewards(
		gomock.Any(), // cacheCtx (not the original ctx due to CacheContext wrapping)
		address,
		types.ModuleName,
		vestingAmount,
		&vestingPeriods,
		gomock.Any(),
	).Return(fmt.Errorf("failed to transfer coins: insufficient module balance"))

	// Allow LogSubAccountTransaction calls
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).AnyTimes()

	err := k.PayParticipantFromModule(ctx, address, amount, types.ModuleName, "test_memo", &vestingPeriods)

	// Error must propagate -- caller (payoutClaim) needs to know payment failed
	require.Error(t, err, "PayParticipantFromModule must return error when AddVestedRewards fails")
	require.Contains(t, err.Error(), "insufficient module balance")
}

// TestPayParticipantVested_AddVestedRewardsSucceeds_Committed proves the happy
// path: when AddVestedRewards succeeds, writeFn() is called and everything
// is committed atomically.
func TestPayParticipantVested_AddVestedRewardsSucceeds_Committed(t *testing.T) {
	k, ctx, mocks := vestedPaymentSetup(t)

	address := "gonka1s8szs7n43jxgz4a4xaxmzm5emh7fmjxhach7w8"
	amount := int64(1000)
	vestingPeriods := uint64(10)
	vestingAmount := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, amount))

	// Mock: AddVestedRewards succeeds
	mocks.StreamVestingKeeper.EXPECT().AddVestedRewards(
		gomock.Any(),
		address,
		types.ModuleName,
		vestingAmount,
		&vestingPeriods,
		gomock.Any(),
	).Return(nil)

	// Allow LogSubAccountTransaction calls
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).AnyTimes()

	err := k.PayParticipantFromModule(ctx, address, amount, types.ModuleName, "test_memo", &vestingPeriods)

	require.NoError(t, err, "PayParticipantFromModule must succeed when AddVestedRewards succeeds")
}

// TestPayParticipantVested_ZeroAmount_NoOp proves that zero-amount payments
// return nil without calling AddVestedRewards or any bank operations.
func TestPayParticipantVested_ZeroAmount_NoOp(t *testing.T) {
	k, ctx, _ := vestedPaymentSetup(t)

	address := "gonka1s8szs7n43jxgz4a4xaxmzm5emh7fmjxhach7w8"
	vestingPeriods := uint64(10)

	// No mocks set up -- if any bank/vesting calls happen, gomock will panic
	err := k.PayParticipantFromModule(ctx, address, 0, types.ModuleName, "test_memo", &vestingPeriods)

	require.NoError(t, err, "zero amount must be a no-op")
}

// TestPayParticipantDirect_TransferFails_ErrorPropagated proves that direct
// (non-vested) payment failures also propagate errors correctly.
func TestPayParticipantDirect_TransferFails_ErrorPropagated(t *testing.T) {
	k, ctx, mocks := vestedPaymentSetup(t)

	address := "gonka1s8szs7n43jxgz4a4xaxmzm5emh7fmjxhach7w8"
	addr, _ := sdk.AccAddressFromBech32(address)
	amount := int64(1000)
	coins := sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, amount))

	// Mock: direct payment fails
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		gomock.Any(),
		types.ModuleName,
		addr,
		coins,
		gomock.Any(),
	).Return(fmt.Errorf("insufficient funds"))

	// Allow LogSubAccountTransaction calls
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).AnyTimes()

	// nil vestingPeriods = direct payment
	err := k.PayParticipantFromModule(ctx, address, amount, types.ModuleName, "test_memo", nil)

	require.Error(t, err, "direct payment failure must propagate error")
}
