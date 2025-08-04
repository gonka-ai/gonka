package keeper_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/collateral/keeper"
	"github.com/productscience/inference/x/collateral/types"
	inftypes "github.com/productscience/inference/x/inference/types"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"go.uber.org/mock/gomock"
)

func setupMsgServer(t testing.TB) (keeper.Keeper, types.MsgServer, context.Context) {
	k, ctx := keepertest.CollateralKeeper(t)
	return k, keeper.NewMsgServerImpl(k), ctx
}

func TestMsgServer(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	require.NotNil(t, ms)
	require.NotNil(t, ctx)
	require.NotEmpty(t, k)
}

func (s *KeeperTestSuite) TestMsgDepositCollateral_Success() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)

	deposit := sdk.NewInt64Coin(inftypes.BaseCoin, 100)

	s.bankKeeper.EXPECT().
		SendCoinsFromAccountToModule(s.ctx, participant, types.ModuleName, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx sdk.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins, memo string) error {
			s.Require().Equal(types.ModuleName, recipientModule)
			s.Require().Equal(participant, senderAddr)
			s.Require().Equal(deposit.Amount, amt.AmountOf(inftypes.BaseCoin))
			s.Require().Equal("collateral deposit", memo)
			return nil
		}).
		Times(1)

	s.bankKeeper.EXPECT().
		LogSubAccountTransaction(s.ctx, types.ModuleName, participantStr, types.SubAccountCollateral, deposit, "collateral deposit").
		Times(1)

	msg := &types.MsgDepositCollateral{
		Participant: participantStr,
		Amount:      deposit,
	}

	_, err = s.msgServer.DepositCollateral(s.ctx, msg)
	s.Require().NoError(err)

	collateral, found := s.k.GetCollateral(s.ctx, participant)
	s.Require().True(found)
	s.Require().Equal(deposit, collateral)
}

func (s *KeeperTestSuite) TestMsgDepositCollateral_Aggregation() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)

	// First deposit
	initialDeposit := sdk.NewInt64Coin(inftypes.BaseCoin, 100)
	s.k.SetCollateral(s.ctx, participant, initialDeposit)

	// Second deposit
	secondDepositAmount := int64(50)
	secondDeposit := sdk.NewInt64Coin(inftypes.BaseCoin, secondDepositAmount)

	s.bankKeeper.EXPECT().
		SendCoinsFromAccountToModule(s.ctx, participant, types.ModuleName, gomock.Any(), gomock.Any()).
		Return(nil).
		Times(1)

	s.bankKeeper.EXPECT().
		LogSubAccountTransaction(s.ctx, types.ModuleName, participantStr, types.SubAccountCollateral, secondDeposit, "collateral deposit").
		Times(1)

	msg := &types.MsgDepositCollateral{
		Participant: participantStr,
		Amount:      secondDeposit,
	}

	_, err = s.msgServer.DepositCollateral(s.ctx, msg)
	s.Require().NoError(err)

	finalCollateral, found := s.k.GetCollateral(s.ctx, participant)
	s.Require().True(found)

	expectedAmount := initialDeposit.Amount.Add(secondDeposit.Amount)
	s.Require().Equal(expectedAmount, finalCollateral.Amount)
	s.Require().Equal(inftypes.BaseCoin, finalCollateral.Denom)
}

func (s *KeeperTestSuite) TestMsgDepositCollateral_InvalidDenom() {
	participantStr := sample.AccAddress()
	invalidDeposit := sdk.NewInt64Coin("faketoken", 100)

	msg := &types.MsgDepositCollateral{
		Participant: participantStr,
		Amount:      invalidDeposit,
	}

	_, err := s.msgServer.DepositCollateral(s.ctx, msg)
	s.Require().Error(err)
	s.Require().ErrorIs(err, types.ErrInvalidDenom)
}

func (s *KeeperTestSuite) TestMsgWithdrawCollateral_Success() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)
	initialAmount := int64(1000)
	withdrawAmount := int64(400)

	// Setup initial collateral
	initialCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, initialAmount)
	s.k.SetCollateral(s.ctx, participant, initialCollateral)

	// Set current epoch
	s.k.SetCurrentEpoch(s.ctx, 10)

	// Withdraw a portion
	withdrawCoin := sdk.NewInt64Coin(inftypes.BaseCoin, withdrawAmount)

	// Expect LogSubAccountTransaction calls
	s.bankKeeper.EXPECT().
		LogSubAccountTransaction(s.ctx, participantStr, types.ModuleName, types.SubAccountCollateral, withdrawCoin, "collateral to unbonding").
		Times(1)

	s.bankKeeper.EXPECT().
		LogSubAccountTransaction(s.ctx, types.ModuleName, participantStr, types.SubAccountUnbonding, withdrawCoin, "collateral to unbonding").
		Times(1)

	msg := &types.MsgWithdrawCollateral{
		Participant: participantStr,
		Amount:      withdrawCoin,
	}

	res, err := s.msgServer.WithdrawCollateral(s.ctx, msg)
	s.Require().NoError(err)

	// Verify completion epoch in response
	params := s.k.GetParams(s.ctx)
	expectedCompletionEpoch := uint64(10) + params.UnbondingPeriodEpochs
	s.Require().Equal(expectedCompletionEpoch, res.CompletionEpoch)

	// Verify remaining active collateral
	remainingCollateral, found := s.k.GetCollateral(s.ctx, participant)
	s.Require().True(found)
	expectedRemaining := initialAmount - withdrawAmount
	s.Require().Equal(math.NewInt(expectedRemaining), remainingCollateral.Amount)

	// Verify unbonding entry
	unbonding, found := s.k.GetUnbondingCollateral(s.ctx, participant, expectedCompletionEpoch)
	s.Require().True(found)
	s.Require().Equal(withdrawCoin, unbonding.Amount)
	s.Require().Equal(participantStr, unbonding.Participant)
}

func (s *KeeperTestSuite) TestMsgWithdrawCollateral_InsufficientFunds() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)
	initialAmount := int64(100)
	withdrawAmount := int64(200)

	// Setup initial collateral
	initialCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, initialAmount)
	s.k.SetCollateral(s.ctx, participant, initialCollateral)

	// Attempt to withdraw more than available
	withdrawCoin := sdk.NewInt64Coin(inftypes.BaseCoin, withdrawAmount)
	msg := &types.MsgWithdrawCollateral{
		Participant: participantStr,
		Amount:      withdrawCoin,
	}

	_, err = s.msgServer.WithdrawCollateral(s.ctx, msg)
	s.Require().Error(err)
	s.Require().ErrorIs(err, types.ErrInsufficientCollateral)
}

func (s *KeeperTestSuite) TestMsgWithdrawCollateral_FullWithdrawal() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)
	initialAmount := int64(1000)

	// Setup initial collateral
	initialCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, initialAmount)
	s.k.SetCollateral(s.ctx, participant, initialCollateral)

	// Set current epoch
	s.k.SetCurrentEpoch(s.ctx, 20)

	// Expect LogSubAccountTransaction calls
	s.bankKeeper.EXPECT().
		LogSubAccountTransaction(s.ctx, participantStr, types.ModuleName, types.SubAccountCollateral, initialCollateral, "collateral to unbonding").
		Times(1)

	s.bankKeeper.EXPECT().
		LogSubAccountTransaction(s.ctx, types.ModuleName, participantStr, types.SubAccountUnbonding, initialCollateral, "collateral to unbonding").
		Times(1)

	// Withdraw all collateral
	msg := &types.MsgWithdrawCollateral{
		Participant: participantStr,
		Amount:      initialCollateral,
	}

	res, err := s.msgServer.WithdrawCollateral(s.ctx, msg)
	s.Require().NoError(err)

	// Verify completion epoch in response
	params := s.k.GetParams(s.ctx)
	expectedCompletionEpoch := uint64(20) + params.UnbondingPeriodEpochs
	s.Require().Equal(expectedCompletionEpoch, res.CompletionEpoch)

	// Verify active collateral is removed
	_, found := s.k.GetCollateral(s.ctx, participant)
	s.Require().False(found, "active collateral record should be removed after full withdrawal")

	// Verify unbonding entry
	unbonding, found := s.k.GetUnbondingCollateral(s.ctx, participant, expectedCompletionEpoch)
	s.Require().True(found)
	s.Require().Equal(initialCollateral, unbonding.Amount)
}

func (s *KeeperTestSuite) TestMsgWithdrawCollateral_UnbondingAggregation() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)
	initialAmount := int64(1000)
	firstWithdrawAmount := int64(200)
	secondWithdrawAmount := int64(300)

	// Setup initial collateral
	initialCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, initialAmount)
	s.k.SetCollateral(s.ctx, participant, initialCollateral)

	// Set current epoch
	currentEpoch := uint64(30)
	s.k.SetCurrentEpoch(s.ctx, currentEpoch)
	params := s.k.GetParams(s.ctx)
	expectedCompletionEpoch := currentEpoch + params.UnbondingPeriodEpochs

	// First withdrawal
	firstWithdrawCoin := sdk.NewInt64Coin(inftypes.BaseCoin, firstWithdrawAmount)

	// Expect LogSubAccountTransaction calls for first withdrawal
	s.bankKeeper.EXPECT().
		LogSubAccountTransaction(s.ctx, participantStr, types.ModuleName, types.SubAccountCollateral, firstWithdrawCoin, "collateral to unbonding").
		Times(1)

	s.bankKeeper.EXPECT().
		LogSubAccountTransaction(s.ctx, types.ModuleName, participantStr, types.SubAccountUnbonding, firstWithdrawCoin, "collateral to unbonding").
		Times(1)

	msg1 := &types.MsgWithdrawCollateral{
		Participant: participantStr,
		Amount:      firstWithdrawCoin,
	}
	_, err = s.msgServer.WithdrawCollateral(s.ctx, msg1)
	s.Require().NoError(err)

	// Second withdrawal in the same epoch
	secondWithdrawCoin := sdk.NewInt64Coin(inftypes.BaseCoin, secondWithdrawAmount)

	// Expect LogSubAccountTransaction calls for second withdrawal
	s.bankKeeper.EXPECT().
		LogSubAccountTransaction(s.ctx, participantStr, types.ModuleName, types.SubAccountCollateral, secondWithdrawCoin, "collateral to unbonding").
		Times(1)

	s.bankKeeper.EXPECT().
		LogSubAccountTransaction(s.ctx, types.ModuleName, participantStr, types.SubAccountUnbonding, secondWithdrawCoin, "collateral to unbonding").
		Times(1)

	msg2 := &types.MsgWithdrawCollateral{
		Participant: participantStr,
		Amount:      secondWithdrawCoin,
	}
	_, err = s.msgServer.WithdrawCollateral(s.ctx, msg2)
	s.Require().NoError(err)

	// Verify unbonding entry is aggregated
	unbonding, found := s.k.GetUnbondingCollateral(s.ctx, participant, expectedCompletionEpoch)
	s.Require().True(found)
	expectedUnbondingAmount := firstWithdrawCoin.Amount.Add(secondWithdrawCoin.Amount)
	s.Require().Equal(expectedUnbondingAmount, unbonding.Amount.Amount)

	// Verify remaining active collateral
	remainingCollateral, found := s.k.GetCollateral(s.ctx, participant)
	s.Require().True(found)
	totalWithdrawn := firstWithdrawAmount + secondWithdrawAmount
	expectedRemaining := initialAmount - totalWithdrawn
	s.Require().Equal(math.NewInt(expectedRemaining), remainingCollateral.Amount)
}
