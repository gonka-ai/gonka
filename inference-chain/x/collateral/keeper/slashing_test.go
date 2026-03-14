package keeper_test

import (
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/collateral/types"
	inftypes "github.com/productscience/inference/x/inference/types"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
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

	s.Require().NoError(s.k.SetCollateral(s.ctx, participant, activeCollateral))
	s.Require().NoError(s.k.AddUnbondingCollateral(s.ctx, participant, completionEpoch, unbondingCollateral))

	slashFraction := math.LegacyNewDecWithPrec(10, 2) // 10%
	totalCollateral := activeAmount + unbondingAmount
	expectedSlashedAmount := math.NewInt(totalCollateral).ToLegacyDec().Mul(slashFraction).TruncateInt()

	// Expect the total slashed amount to be redirected to governance
	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToModule(s.ctx, types.ModuleName, govtypes.ModuleName, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx sdk.Context, senderModule, recipientModule string, amt sdk.Coins, memo string) error {
			s.Require().Equal(types.ModuleName, senderModule)
			s.Require().Equal(govtypes.ModuleName, recipientModule)
			s.Require().Equal(expectedSlashedAmount, amt.AmountOf(inftypes.BaseCoin))
			s.Require().Equal("collateral_slashed:invalidation", memo)
			return nil
		}).
		Times(1)

	// Perform the slash
	slashedAmount, err := s.k.Slash(s.ctx, participant, slashFraction, inftypes.SlashReasonInvalidation, math.ZeroInt())
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
	s.Require().NoError(s.k.SetCollateral(s.ctx, participant, activeCollateral))

	slashFraction := math.LegacyNewDecWithPrec(20, 2) // 20%
	expectedSlashedAmount := math.NewInt(activeAmount).ToLegacyDec().Mul(slashFraction).TruncateInt()

	// Expect the total slashed amount to be redirected to governance
	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToModule(s.ctx, types.ModuleName, govtypes.ModuleName, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx sdk.Context, senderModule, recipientModule string, amt sdk.Coins, memo string) error {
			s.Require().Equal(types.ModuleName, senderModule)
			s.Require().Equal(govtypes.ModuleName, recipientModule)
			s.Require().Equal(expectedSlashedAmount, amt.AmountOf(inftypes.BaseCoin))
			s.Require().Equal("collateral_slashed:invalidation", memo)
			return nil
		}).
		Times(1)

	// Perform the slash
	slashedAmount, err := s.k.Slash(s.ctx, participant, slashFraction, inftypes.SlashReasonInvalidation, math.ZeroInt())
	s.Require().NoError(err)
	s.Require().Equal(expectedSlashedAmount, slashedAmount.Amount)

	// Verify active collateral was slashed
	expectedActive := activeCollateral.Amount.ToLegacyDec().Mul(math.LegacyNewDec(1).Sub(slashFraction)).TruncateInt()
	newActive, found := s.k.GetCollateral(s.ctx, participant)
	s.Require().True(found)
	s.Require().Equal(expectedActive, newActive.Amount)

	// Verify no unbonding entries were created or affected
	unbondingEntries, err := s.k.GetUnbondingByParticipant(s.ctx, participant)
	s.Require().NoError(err)
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
	s.Require().NoError(s.k.AddUnbondingCollateral(s.ctx, participant, completionEpoch, unbondingCollateral))

	slashFraction := math.LegacyNewDecWithPrec(50, 2) // 50%
	expectedSlashedAmount := math.NewInt(unbondingAmount).ToLegacyDec().Mul(slashFraction).TruncateInt()

	// Expect the total slashed amount to be redirected to governance
	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToModule(s.ctx, types.ModuleName, govtypes.ModuleName, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx sdk.Context, senderModule, recipientModule string, amt sdk.Coins, memo string) error {
			s.Require().Equal(types.ModuleName, senderModule)
			s.Require().Equal(govtypes.ModuleName, recipientModule)
			s.Require().Equal(expectedSlashedAmount, amt.AmountOf(inftypes.BaseCoin))
			s.Require().Equal("collateral_slashed:invalidation", memo)
			return nil
		}).
		Times(1)

	// Perform the slash
	slashedAmount, err := s.k.Slash(s.ctx, participant, slashFraction, inftypes.SlashReasonInvalidation, math.ZeroInt())
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
	s.Require().NoError(s.k.SetCollateral(s.ctx, participant, initialCollateral))

	// Case 1: Negative fraction
	_, err = s.k.Slash(s.ctx, participant, math.LegacyNewDec(-1), inftypes.SlashReasonInvalidation, math.ZeroInt())
	s.Require().Error(err, "should error on negative slash fraction")

	// Case 2: Fraction greater than 1
	_, err = s.k.Slash(s.ctx, participant, math.LegacyNewDec(2), inftypes.SlashReasonInvalidation, math.ZeroInt())
	s.Require().Error(err, "should error on slash fraction greater than 1")

	// Verify collateral is unchanged
	finalCollateral, found := s.k.GetCollateral(s.ctx, participant)
	s.Require().True(found)
	s.Require().Equal(initialCollateral, finalCollateral)
}

// ---------------------------------------------------------------------------
// Tests for requiredCollateral-based slashing (tokenomics-v2)
// ---------------------------------------------------------------------------

// TestSlashing_RequiredCollateral_OverDeposit verifies that an over-depositor
// (actual > required) is only penalized on the required amount.
// Scenario: active=200, required=80, fraction=10%
// => slashTarget = min(80, 200) * 10% = 8
// => effectiveFraction = 8/200 = 4%
// => slashed = 200 * 4% = 8
func (s *KeeperTestSuite) TestSlashing_RequiredCollateral_OverDeposit() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)

	activeAmount := int64(200)
	activeCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, activeAmount)
	s.Require().NoError(s.k.SetCollateral(s.ctx, participant, activeCollateral))

	slashFraction := math.LegacyNewDecWithPrec(10, 2) // 10%
	requiredCollateral := math.NewInt(80)

	// Expected: min(80, 200) * 10% = 8
	expectedSlashed := math.NewInt(8)

	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToModule(s.ctx, types.ModuleName, govtypes.ModuleName, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx sdk.Context, senderModule, recipientModule string, amt sdk.Coins, memo string) error {
			s.Require().Equal(types.ModuleName, senderModule)
			s.Require().Equal(govtypes.ModuleName, recipientModule)
			s.Require().Equal(expectedSlashed, amt.AmountOf(inftypes.BaseCoin))
			s.Require().Equal("collateral_slashed:invalidation", memo)
			return nil
		}).
		Times(1)

	slashed, err := s.k.Slash(s.ctx, participant, slashFraction, inftypes.SlashReasonInvalidation, requiredCollateral)
	s.Require().NoError(err)
	s.Require().Equal(expectedSlashed, slashed.Amount,
		"over-depositor should be slashed only from required amount")

	// Remaining = 200 - 8 = 192
	remaining, found := s.k.GetCollateral(s.ctx, participant)
	s.Require().True(found)
	s.Require().Equal(math.NewInt(192), remaining.Amount)
}

// TestSlashing_RequiredCollateral_UnderDeposit verifies that an under-depositor
// (actual < required) is slashed from the actual balance, not the required amount.
// Scenario: active=50, required=80, fraction=10%
// => slashTarget = min(80, 50) * 10% = 5
// => effectiveFraction = 5/50 = 10%
// => slashed = 50 * 10% = 5
func (s *KeeperTestSuite) TestSlashing_RequiredCollateral_UnderDeposit() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)

	activeAmount := int64(50)
	activeCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, activeAmount)
	s.Require().NoError(s.k.SetCollateral(s.ctx, participant, activeCollateral))

	slashFraction := math.LegacyNewDecWithPrec(10, 2) // 10%
	requiredCollateral := math.NewInt(80)

	// Expected: min(80, 50) * 10% = 5
	expectedSlashed := math.NewInt(5)

	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToModule(s.ctx, types.ModuleName, govtypes.ModuleName, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx sdk.Context, senderModule, recipientModule string, amt sdk.Coins, memo string) error {
			s.Require().Equal(types.ModuleName, senderModule)
			s.Require().Equal(govtypes.ModuleName, recipientModule)
			s.Require().Equal(expectedSlashed, amt.AmountOf(inftypes.BaseCoin))
			s.Require().Equal("collateral_slashed:invalidation", memo)
			return nil
		}).
		Times(1)

	slashed, err := s.k.Slash(s.ctx, participant, slashFraction, inftypes.SlashReasonInvalidation, requiredCollateral)
	s.Require().NoError(err)
	s.Require().Equal(expectedSlashed, slashed.Amount,
		"under-depositor should be slashed from actual balance (capped by actual)")

	// Remaining = 50 - 5 = 45
	remaining, found := s.k.GetCollateral(s.ctx, participant)
	s.Require().True(found)
	s.Require().Equal(math.NewInt(45), remaining.Amount)
}

// TestSlashing_RequiredCollateral_ExactMatch verifies that when actual == required,
// the slash is identical to legacy behavior.
// Scenario: active=100, required=100, fraction=10% => slashed=10
func (s *KeeperTestSuite) TestSlashing_RequiredCollateral_ExactMatch() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)

	activeAmount := int64(100)
	activeCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, activeAmount)
	s.Require().NoError(s.k.SetCollateral(s.ctx, participant, activeCollateral))

	slashFraction := math.LegacyNewDecWithPrec(10, 2) // 10%
	requiredCollateral := math.NewInt(100)

	expectedSlashed := math.NewInt(10)

	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToModule(s.ctx, types.ModuleName, govtypes.ModuleName, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx sdk.Context, senderModule, recipientModule string, amt sdk.Coins, memo string) error {
			s.Require().Equal(types.ModuleName, senderModule)
			s.Require().Equal(govtypes.ModuleName, recipientModule)
			s.Require().Equal(expectedSlashed, amt.AmountOf(inftypes.BaseCoin))
			s.Require().Equal("collateral_slashed:invalidation", memo)
			return nil
		}).
		Times(1)

	slashed, err := s.k.Slash(s.ctx, participant, slashFraction, inftypes.SlashReasonInvalidation, requiredCollateral)
	s.Require().NoError(err)
	s.Require().Equal(expectedSlashed, slashed.Amount,
		"exact match should behave like legacy")
}

// TestSlashing_RequiredCollateral_Zero_LegacyBehavior verifies that
// requiredCollateral=0 preserves the original behavior (slash from full balance).
// Scenario: active=200, required=0, fraction=10% => slashed=20 (not 8)
func (s *KeeperTestSuite) TestSlashing_RequiredCollateral_Zero_LegacyBehavior() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)

	activeAmount := int64(200)
	activeCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, activeAmount)
	s.Require().NoError(s.k.SetCollateral(s.ctx, participant, activeCollateral))

	slashFraction := math.LegacyNewDecWithPrec(10, 2) // 10%

	// With requiredCollateral=0, legacy: 200 * 10% = 20
	expectedSlashed := math.NewInt(20)

	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToModule(s.ctx, types.ModuleName, govtypes.ModuleName, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx sdk.Context, senderModule, recipientModule string, amt sdk.Coins, memo string) error {
			s.Require().Equal(types.ModuleName, senderModule)
			s.Require().Equal(govtypes.ModuleName, recipientModule)
			s.Require().Equal(expectedSlashed, amt.AmountOf(inftypes.BaseCoin))
			s.Require().Equal("collateral_slashed:invalidation", memo)
			return nil
		}).
		Times(1)

	slashed, err := s.k.Slash(s.ctx, participant, slashFraction, inftypes.SlashReasonInvalidation, math.ZeroInt())
	s.Require().NoError(err)
	s.Require().Equal(expectedSlashed, slashed.Amount,
		"zero requiredCollateral must use legacy (full balance) behavior")
}

// TestSlashing_RequiredCollateral_WithUnbonding verifies proportional slashing
// across active and unbonding collateral with requiredCollateral.
// Scenario: active=150, unbonding=50, total=200, required=80, fraction=10%
// => slashTarget = min(80, 200) * 10% = 8
// => effectiveFraction = 8/200 = 4%
// => active slashed = 150 * 4% = 6, unbonding slashed = 50 * 4% = 2
func (s *KeeperTestSuite) TestSlashing_RequiredCollateral_WithUnbonding() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)

	activeCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, 150)
	unbondingCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, 50)
	completionEpoch := uint64(100)

	s.Require().NoError(s.k.SetCollateral(s.ctx, participant, activeCollateral))
	s.Require().NoError(s.k.AddUnbondingCollateral(s.ctx, participant, completionEpoch, unbondingCollateral))

	slashFraction := math.LegacyNewDecWithPrec(10, 2) // 10%
	requiredCollateral := math.NewInt(80)

	// Total slashed = min(80, 200) * 10% = 8
	expectedSlashed := math.NewInt(8)

	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToModule(s.ctx, types.ModuleName, govtypes.ModuleName, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx sdk.Context, senderModule, recipientModule string, amt sdk.Coins, memo string) error {
			s.Require().Equal(types.ModuleName, senderModule)
			s.Require().Equal(govtypes.ModuleName, recipientModule)
			s.Require().Equal(expectedSlashed, amt.AmountOf(inftypes.BaseCoin))
			s.Require().Equal("collateral_slashed:invalidation", memo)
			return nil
		}).
		Times(1)

	slashed, err := s.k.Slash(s.ctx, participant, slashFraction, inftypes.SlashReasonInvalidation, requiredCollateral)
	s.Require().NoError(err)
	s.Require().Equal(expectedSlashed, slashed.Amount)

	// effectiveFraction = 8/200 = 0.04
	// active remaining = 150 - (150 * 0.04) = 150 - 6 = 144
	newActive, found := s.k.GetCollateral(s.ctx, participant)
	s.Require().True(found)
	s.Require().Equal(math.NewInt(144), newActive.Amount)

	// unbonding remaining = 50 - (50 * 0.04) = 50 - 2 = 48
	newUnbonding, found := s.k.GetUnbondingCollateral(s.ctx, participant, completionEpoch)
	s.Require().True(found)
	s.Require().Equal(math.NewInt(48), newUnbonding.Amount.Amount)
}
