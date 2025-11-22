package keeper_test

import (
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/collateral/types"
	inftypes "github.com/productscience/inference/x/inference/types"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"go.uber.org/mock/gomock"
)

func (s *KeeperTestSuite) TestSlashing_Proportional() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)

	// Setup collateral state
	activeAmount := int64(1000)
	unbondingAmount := int64(500)
	activeCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, activeAmount)
	unbondingCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, unbondingAmount)
	completionEpoch := uint64(100)

	s.k.SetCollateral(s.ctx, participant, activeCollateral)
	s.k.AddUnbondingCollateral(s.ctx, participant, completionEpoch, unbondingCollateral)

	slashFraction := math.LegacyNewDecWithPrec(10, 2) // 10%
	totalCollateral := activeAmount + unbondingAmount
	expectedSlashedAmount := math.NewInt(totalCollateral).ToLegacyDec().Mul(slashFraction).TruncateInt()

	// Expect the total slashed amount to be burned from the module account
	s.bankKeeper.EXPECT().
		BurnCoins(s.ctx, types.ModuleName, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx sdk.Context, moduleName string, amt sdk.Coins, memo string) error {
			s.Require().Equal(types.ModuleName, moduleName)
			s.Require().Equal(expectedSlashedAmount, amt.AmountOf(inftypes.BaseCoin))
			s.Require().Equal("collateral_slashed:invalidation", memo)
			return nil
		}).
		Times(1)

	// Perform the slash
	slashedAmount, err := s.k.Slash(s.ctx, participant, slashFraction, inftypes.SlashReasonInvalidation)
	s.Require().NoError(err)
	s.Require().Equal(expectedSlashedAmount, slashedAmount.Amount)

	// Verify active collateral was slashed
	expectedActive := activeCollateral.Amount.ToLegacyDec().Mul(math.LegacyNewDec(1).Sub(slashFraction)).TruncateInt()
	newActive, found := s.k.GetCollateral(s.ctx, participant)
	s.Require().True(found)
	s.Require().Equal(expectedActive, newActive.Amount)

	// Verify unbonding collateral was slashed
	expectedUnbonding := unbondingCollateral.Amount.ToLegacyDec().Mul(math.LegacyNewDec(1).Sub(slashFraction)).TruncateInt()
	newUnbonding, found := s.k.GetUnbondingCollateral(s.ctx, participant, completionEpoch)
	s.Require().True(found)
	s.Require().Equal(expectedUnbonding, newUnbonding.Amount.Amount)
}

func (s *KeeperTestSuite) TestSlashing_ActiveOnly() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)

	// Setup collateral state
	activeAmount := int64(1000)
	activeCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, activeAmount)
	s.k.SetCollateral(s.ctx, participant, activeCollateral)

	slashFraction := math.LegacyNewDecWithPrec(20, 2) // 20%
	expectedSlashedAmount := math.NewInt(activeAmount).ToLegacyDec().Mul(slashFraction).TruncateInt()

	// Expect the total slashed amount to be burned
	s.bankKeeper.EXPECT().
		BurnCoins(s.ctx, types.ModuleName, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx sdk.Context, moduleName string, amt sdk.Coins, memo string) error {
			s.Require().Equal(expectedSlashedAmount, amt.AmountOf(inftypes.BaseCoin))
			s.Require().Equal("collateral_slashed:invalidation", memo)
			return nil
		}).
		Times(1)

	// Perform the slash
	slashedAmount, err := s.k.Slash(s.ctx, participant, slashFraction, inftypes.SlashReasonInvalidation)
	s.Require().NoError(err)
	s.Require().Equal(expectedSlashedAmount, slashedAmount.Amount)

	// Verify active collateral was slashed
	expectedActive := activeCollateral.Amount.ToLegacyDec().Mul(math.LegacyNewDec(1).Sub(slashFraction)).TruncateInt()
	newActive, found := s.k.GetCollateral(s.ctx, participant)
	s.Require().True(found)
	s.Require().Equal(expectedActive, newActive.Amount)

	// Verify no unbonding entries were created or affected
	unbondingEntries := s.k.GetUnbondingByParticipant(s.ctx, participant)
	s.Require().Empty(unbondingEntries)
}

func (s *KeeperTestSuite) TestSlashing_UnbondingOnly() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)
	// Setup collateral state with only unbonding collateral
	unbondingAmount := int64(500)
	unbondingCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, unbondingAmount)
	completionEpoch := uint64(100)
	s.k.AddUnbondingCollateral(s.ctx, participant, completionEpoch, unbondingCollateral)

	slashFraction := math.LegacyNewDecWithPrec(50, 2) // 50%
	expectedSlashedAmount := math.NewInt(unbondingAmount).ToLegacyDec().Mul(slashFraction).TruncateInt()

	// Expect the total slashed amount to be burned
	s.bankKeeper.EXPECT().
		BurnCoins(s.ctx, types.ModuleName, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx sdk.Context, moduleName string, amt sdk.Coins, memo string) error {
			s.Require().Equal(expectedSlashedAmount, amt.AmountOf(inftypes.BaseCoin))
			s.Require().Equal("collateral_slashed:invalidation", memo)
			return nil
		}).
		Times(1)

	// Perform the slash
	slashedAmount, err := s.k.Slash(s.ctx, participant, slashFraction, inftypes.SlashReasonInvalidation)
	s.Require().NoError(err)
	s.Require().Equal(expectedSlashedAmount, slashedAmount.Amount)

	// Verify unbonding collateral was slashed
	expectedUnbonding := unbondingCollateral.Amount.ToLegacyDec().Mul(math.LegacyNewDec(1).Sub(slashFraction)).TruncateInt()
	newUnbonding, found := s.k.GetUnbondingCollateral(s.ctx, participant, completionEpoch)
	s.Require().True(found)
	s.Require().Equal(expectedUnbonding, newUnbonding.Amount.Amount)

	// Verify no active collateral was created or affected
	_, found = s.k.GetCollateral(s.ctx, participant)
	s.Require().False(found)
}

func (s *KeeperTestSuite) TestSlashing_InvalidFraction() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)

	// Setup collateral state
	initialCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, 1000)
	s.k.SetCollateral(s.ctx, participant, initialCollateral)

	// Case 1: Negative fraction
	_, err = s.k.Slash(s.ctx, participant, math.LegacyNewDec(-1), inftypes.SlashReasonInvalidation)
	s.Require().Error(err, "should error on negative slash fraction")

	// Case 2: Fraction greater than 1
	_, err = s.k.Slash(s.ctx, participant, math.LegacyNewDec(2), inftypes.SlashReasonInvalidation)
	s.Require().Error(err, "should error on slash fraction greater than 1")

	// Verify collateral is unchanged
	finalCollateral, found := s.k.GetCollateral(s.ctx, participant)
	s.Require().True(found)
	s.Require().Equal(initialCollateral, finalCollateral)
}
