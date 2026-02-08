package keeper_test

import (
	"github.com/productscience/inference/testutil/sample"
	collateralmodule "github.com/productscience/inference/x/collateral/module"
	"github.com/productscience/inference/x/collateral/types"
	inftypes "github.com/productscience/inference/x/inference/types"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"go.uber.org/mock/gomock"
)

func (s *KeeperTestSuite) TestStakingHooks_BeforeValidatorSlashed() {
	// Setup - create a validator address and its corresponding account address
	valAddr, accAddr := sample.AccAddressAndValAddress()
	accAddr, err := sdk.AccAddressFromBech32(accAddr.String())
	s.Require().NoError(err)

	// Setup collateral for the participant
	initialAmount := int64(1000)
	initialCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, initialAmount)
	s.Require().NoError(s.k.SetCollateral(s.ctx, accAddr, initialCollateral))

	// Define the slash
	slashFraction := math.LegacyNewDecWithPrec(25, 2) // 25%
	expectedSlashedAmount := math.NewInt(initialAmount).ToLegacyDec().Mul(slashFraction).TruncateInt()

	// The hook will trigger our Slash function, which in turn will call BurnCoins
	s.bankKeeper.EXPECT().
		BurnCoins(s.ctx, types.ModuleName, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx sdk.Context, moduleName string, amt sdk.Coins, memo string) error {
			s.Require().Equal(expectedSlashedAmount, amt.AmountOf(inftypes.BaseCoin))
			s.Require().Equal("collateral_slashed:", memo)
			return nil
		}).
		Times(1)

	// Trigger the hook
	hooks := collateralmodule.NewStakingHooks(s.k)
	err = hooks.BeforeValidatorSlashed(s.ctx, valAddr, slashFraction)
	s.Require().NoError(err)

	// Verify the collateral was slashed
	finalCollateral, found := s.k.GetCollateral(s.ctx, accAddr)
	s.Require().True(found)
	expectedFinalAmount := initialCollateral.Amount.ToLegacyDec().Mul(math.LegacyNewDec(1).Sub(slashFraction)).TruncateInt()
	s.Require().Equal(expectedFinalAmount, finalCollateral.Amount)
}

func (s *KeeperTestSuite) TestStakingHooks_JailingAndUnjailing() {
	// Setup - create a validator address and its corresponding account address
	valAddr, accAddr := sample.AccAddressAndValAddress()

	hooks := collateralmodule.NewStakingHooks(s.k)

	// 1. Test jailing
	err := hooks.AfterValidatorBeginUnbonding(s.ctx, nil, valAddr)
	s.Require().NoError(err)

	isJailed, err := s.k.IsJailed(s.ctx, accAddr)
	s.Require().NoError(err)
	s.Require().True(isJailed, "participant should be marked as jailed")

	// 2. Test un-jailing
	err = hooks.AfterValidatorBonded(s.ctx, nil, valAddr)
	s.Require().NoError(err)

	isJailed, err = s.k.IsJailed(s.ctx, accAddr)
	s.Require().NoError(err)
	s.Require().False(isJailed, "participant should be un-jailed")
}

func (s *KeeperTestSuite) TestStakingHooks_AfterValidatorRemoved() {
	// Setup - create a validator address and its corresponding account address
	valAddr, accAddr := sample.AccAddressAndValAddress()

	// Setup collateral for the participant
	initialCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, int64(1000))
	s.Require().NoError(s.k.SetCollateral(s.ctx, accAddr, initialCollateral))

	// Setup jailed status
	s.Require().NoError(s.k.SetJailed(s.ctx, accAddr))

	// Setup unbonding entries across multiple epochs
	s.Require().NoError(s.k.AddUnbondingCollateral(s.ctx, accAddr, uint64(5), sdk.NewInt64Coin(inftypes.BaseCoin, int64(100))))
	s.Require().NoError(s.k.AddUnbondingCollateral(s.ctx, accAddr, uint64(10), sdk.NewInt64Coin(inftypes.BaseCoin, int64(200))))
	s.Require().NoError(s.k.AddUnbondingCollateral(s.ctx, accAddr, uint64(15), sdk.NewInt64Coin(inftypes.BaseCoin, int64(300))))

	// Verify all data exists before removal
	_, found := s.k.GetCollateral(s.ctx, accAddr)
	s.Require().True(found, "collateral should exist before removal")

	isJailed, err := s.k.IsJailed(s.ctx, accAddr)
	s.Require().NoError(err)
	s.Require().True(isJailed, "participant should be jailed before removal")

	unbondingEntries, err := s.k.GetUnbondingByParticipant(s.ctx, accAddr)
	s.Require().NoError(err)
	s.Require().Len(unbondingEntries, 3, "should have 3 unbonding entries before removal")

	// Trigger the hook
	hooks := collateralmodule.NewStakingHooks(s.k)
	err = hooks.AfterValidatorRemoved(s.ctx, nil, valAddr)
	s.Require().NoError(err)

	// Verify all data is cleaned up
	_, found = s.k.GetCollateral(s.ctx, accAddr)
	s.Require().False(found, "collateral should be removed after validator removal")

	isJailed, err = s.k.IsJailed(s.ctx, accAddr)
	s.Require().NoError(err)
	s.Require().False(isJailed, "jailed status should be removed after validator removal")

	unbondingEntries, err = s.k.GetUnbondingByParticipant(s.ctx, accAddr)
	s.Require().NoError(err)
	s.Require().Empty(unbondingEntries, "all unbonding entries should be removed after validator removal")
}

func (s *KeeperTestSuite) TestStakingHooks_AfterValidatorRemoved_NoData() {
	// Test that the hook handles the case where validator has no associated data gracefully
	valAddr, accAddr := sample.AccAddressAndValAddress()

	// Verify no data exists before calling the hook
	_, found := s.k.GetCollateral(s.ctx, accAddr)
	s.Require().False(found, "should have no collateral")

	isJailed, err := s.k.IsJailed(s.ctx, accAddr)
	s.Require().NoError(err)
	s.Require().False(isJailed, "should not be jailed")

	unbondingEntries, err := s.k.GetUnbondingByParticipant(s.ctx, accAddr)
	s.Require().NoError(err)
	s.Require().Empty(unbondingEntries, "should have no unbonding entries")

	// Trigger the hook - should succeed without errors
	hooks := collateralmodule.NewStakingHooks(s.k)
	err = hooks.AfterValidatorRemoved(s.ctx, nil, valAddr)
	s.Require().NoError(err, "hook should succeed even when no data exists")
}

func (s *KeeperTestSuite) TestStakingHooks_AfterValidatorRemoved_PartialData() {
	// Test that the hook handles the case where validator has only some types of data
	valAddr, accAddr := sample.AccAddressAndValAddress()

	// Setup only unbonding entries, no collateral or jailed status
	s.Require().NoError(s.k.AddUnbondingCollateral(s.ctx, accAddr, uint64(5), sdk.NewInt64Coin(inftypes.BaseCoin, int64(100))))

	// Verify state before calling the hook
	_, found := s.k.GetCollateral(s.ctx, accAddr)
	s.Require().False(found, "should have no collateral")

	unbondingEntries, err := s.k.GetUnbondingByParticipant(s.ctx, accAddr)
	s.Require().NoError(err)
	s.Require().Len(unbondingEntries, 1, "should have 1 unbonding entry")

	// Trigger the hook
	hooks := collateralmodule.NewStakingHooks(s.k)
	err = hooks.AfterValidatorRemoved(s.ctx, nil, valAddr)
	s.Require().NoError(err)

	// Verify unbonding entry is removed
	unbondingEntries, err = s.k.GetUnbondingByParticipant(s.ctx, accAddr)
	s.Require().NoError(err)
	s.Require().Empty(unbondingEntries, "unbonding entries should be removed")
}
