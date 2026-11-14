package keeper_test

import (
	"context"
	"errors"

	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/collateral/keeper"
	"github.com/productscience/inference/x/collateral/types"
	inftypes "github.com/productscience/inference/x/inference/types"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"go.uber.org/mock/gomock"
)

func (s *KeeperTestSuite) TestEpochProcessing_ProcessUnbondingQueue() {
	// Setup participants and their unbonding amounts
	participant1Str := sample.AccAddress()
	participant1, _ := sdk.AccAddressFromBech32(participant1Str)
	unbondingAmount1 := math.NewInt(100)

	participant2Str := sample.AccAddress()
	participant2, _ := sdk.AccAddressFromBech32(participant2Str)
	unbondingAmount2 := math.NewInt(200)

	completedEpoch := uint64(42)
	unbondingAmount1Coin := sdk.NewCoin(inftypes.BaseCoin, unbondingAmount1)
	unbondingAmount2Coin := sdk.NewCoin(inftypes.BaseCoin, unbondingAmount2)

	// Create unbonding entries
	s.Require().NoError(s.k.AddUnbondingCollateral(s.ctx, participant1, completedEpoch, unbondingAmount1Coin))
	s.Require().NoError(s.k.AddUnbondingCollateral(s.ctx, participant2, completedEpoch, unbondingAmount2Coin))

	// Another unbonding for a future epoch that should NOT be processed
	futureEpoch := completedEpoch + 1
	futureParticipant, _ := sdk.AccAddressFromBech32(sample.AccAddress())
	unbondingFutureCoin := sdk.NewInt64Coin(inftypes.BaseCoin, 50)
	s.Require().NoError(s.k.AddUnbondingCollateral(s.ctx, futureParticipant, futureEpoch, unbondingFutureCoin))

	// Set mock expectations for fund transfers
	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(s.ctx, types.ModuleName, participant1, gomock.Eq(sdk.NewCoins(unbondingAmount1Coin)), gomock.Any()).
		Return(nil).
		Times(1)
	s.bankKeeper.EXPECT().
		LogSubAccountTransaction(s.ctx, participant1.String(), types.ModuleName, types.SubAccountUnbonding, gomock.Eq(unbondingAmount1Coin), gomock.Any())
	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(s.ctx, types.ModuleName, participant2, gomock.Eq(sdk.NewCoins(unbondingAmount2Coin)), gomock.Any()).
		Return(nil).
		Times(1)
	s.bankKeeper.EXPECT().
		LogSubAccountTransaction(s.ctx, participant2.String(), types.ModuleName, types.SubAccountUnbonding, gomock.Eq(unbondingAmount2Coin), gomock.Any())

	// Run the epoch processing
	s.Require().NoError(s.k.AdvanceEpoch(s.ctx, completedEpoch))

	// Verify that the processed unbonding entries are gone
	_, found := s.k.GetUnbondingCollateral(s.ctx, participant1, completedEpoch)
	s.Require().False(found, "processed unbonding entry 1 should be removed")
	_, found = s.k.GetUnbondingCollateral(s.ctx, participant2, completedEpoch)
	s.Require().False(found, "processed unbonding entry 2 should be removed")

	// Verify the future-dated entry is still there
	_, found = s.k.GetUnbondingCollateral(s.ctx, futureParticipant, futureEpoch)
	s.Require().True(found, "future-dated unbonding entry should not be processed")
}

func (s *KeeperTestSuite) TestEpochProcessing_SlashBeforeUnbondingRelease() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)

	activeAmount := int64(20)
	unbondingAmount := int64(80)
	activeCoin := sdk.NewInt64Coin(inftypes.BaseCoin, activeAmount)
	unbondingCoin := sdk.NewInt64Coin(inftypes.BaseCoin, unbondingAmount)
	completedEpoch := uint64(7)
	slashFraction := math.LegacyNewDecWithPrec(50, 2)

	s.Require().NoError(s.k.SetCollateral(s.ctx, participant, activeCoin))
	s.Require().NoError(s.k.AddUnbondingCollateral(s.ctx, participant, completedEpoch, unbondingCoin))

	expectedSlashed := math.NewInt(50)
	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToModule(s.ctx, types.ModuleName, govtypes.ModuleName, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx sdk.Context, senderModule, recipientModule string, amt sdk.Coins, memo string) error {
			s.Require().Equal(expectedSlashed, amt.AmountOf(inftypes.BaseCoin))
			return nil
		}).
		Times(1)

	slashed, err := s.k.Slash(s.ctx, participant, slashFraction, inftypes.SlashReasonInvalidation, math.ZeroInt())
	s.Require().NoError(err)
	s.Require().Equal(expectedSlashed, slashed.Amount)

	postSlashUnbondingAmount := math.NewInt(unbondingAmount / 2)
	postSlashUnbonding, found := s.k.GetUnbondingCollateral(s.ctx, participant, completedEpoch)
	s.Require().True(found)
	s.Require().Equal(postSlashUnbondingAmount, postSlashUnbonding.Amount.Amount)

	expectedReleasedCoin := sdk.NewCoin(inftypes.BaseCoin, postSlashUnbondingAmount)
	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(s.ctx, types.ModuleName, participant, gomock.Eq(sdk.NewCoins(expectedReleasedCoin)), gomock.Any()).
		Return(nil).
		Times(1)
	s.bankKeeper.EXPECT().
		LogSubAccountTransaction(s.ctx, participantStr, types.ModuleName, types.SubAccountUnbonding, gomock.Eq(expectedReleasedCoin), gomock.Any()).
		Times(1)

	s.Require().NoError(s.k.AdvanceEpoch(s.ctx, completedEpoch))

	_, found = s.k.GetUnbondingCollateral(s.ctx, participant, completedEpoch)
	s.Require().False(found)

	postSlashActive, found := s.k.GetCollateral(s.ctx, participant)
	s.Require().True(found)
	s.Require().Equal(math.NewInt(activeAmount/2), postSlashActive.Amount)
}

func (s *KeeperTestSuite) TestEpochProcessing_UnbondingQueueBoundedPerCall() {
	completedEpoch := uint64(42)
	extra := 5
	coin := sdk.NewInt64Coin(inftypes.BaseCoin, 1)
	for i := 0; i < keeper.MaxUnbondingsPerCall+extra; i++ {
		addr, err := sdk.AccAddressFromBech32(sample.AccAddress())
		s.Require().NoError(err)
		s.Require().NoError(s.k.AddUnbondingCollateral(s.ctx, addr, completedEpoch, coin))
	}
	futureParticipant, err := sdk.AccAddressFromBech32(sample.AccAddress())
	s.Require().NoError(err)
	s.Require().NoError(s.k.AddUnbondingCollateral(s.ctx, futureParticipant, completedEpoch+1, coin))

	released := 0
	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Eq(sdk.NewCoins(coin)), gomock.Any()).
		DoAndReturn(func(ctx context.Context, senderModule string, recipient sdk.AccAddress, amt sdk.Coins, memo string) error {
			released++
			return nil
		}).
		AnyTimes()
	s.bankKeeper.EXPECT().
		LogSubAccountTransaction(gomock.Any(), gomock.Any(), types.ModuleName, types.SubAccountUnbonding, gomock.Any(), gomock.Any()).
		AnyTimes()

	// The epoch transition releases at most one block's worth of entries.
	s.Require().NoError(s.k.AdvanceEpoch(s.ctx, completedEpoch))
	s.Require().Equal(keeper.MaxUnbondingsPerCall, released)
	left, err := s.k.GetUnbondingByEpoch(s.ctx, completedEpoch)
	s.Require().NoError(err)
	s.Require().Len(left, extra)

	// The next block releases the rest and leaves the future epoch alone.
	s.Require().NoError(s.k.ProcessMaturedUnbondings(s.ctx))
	s.Require().Equal(keeper.MaxUnbondingsPerCall+extra, released)
	left, err = s.k.GetUnbondingByEpoch(s.ctx, completedEpoch)
	s.Require().NoError(err)
	s.Require().Empty(left)
	_, found := s.k.GetUnbondingCollateral(s.ctx, futureParticipant, completedEpoch+1)
	s.Require().True(found, "future-dated unbonding entry should not be processed")

	// Nothing matured is left: another block is a no-op.
	s.Require().NoError(s.k.ProcessMaturedUnbondings(s.ctx))
	s.Require().Equal(keeper.MaxUnbondingsPerCall+extra, released)
}

func (s *KeeperTestSuite) TestEpochProcessing_FailedPayoutKeepsEarlierRemovals() {
	completedEpoch := uint64(42)
	coin := sdk.NewInt64Coin(inftypes.BaseCoin, 1)
	for i := 0; i < 3; i++ {
		addr, err := sdk.AccAddressFromBech32(sample.AccAddress())
		s.Require().NoError(err)
		s.Require().NoError(s.k.AddUnbondingCollateral(s.ctx, addr, completedEpoch, coin))
	}

	calls := 0
	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, senderModule string, recipient sdk.AccAddress, amt sdk.Coins, memo string) error {
			calls++
			if calls == 2 {
				return errors.New("module account underfunded")
			}
			return nil
		}).
		AnyTimes()
	s.bankKeeper.EXPECT().
		LogSubAccountTransaction(gomock.Any(), gomock.Any(), types.ModuleName, types.SubAccountUnbonding, gomock.Any(), gomock.Any()).
		AnyTimes()

	s.Require().Error(s.k.AdvanceEpoch(s.ctx, completedEpoch))
	// The entry paid before the failure is gone, so a retry cannot pay it again.
	left, err := s.k.GetUnbondingByEpoch(s.ctx, completedEpoch)
	s.Require().NoError(err)
	s.Require().Len(left, 2)
}
