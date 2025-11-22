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
	s.k.SetCollateral(s.ctx, accAddr, initialCollateral)

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

	isJailed := s.k.IsJailed(s.ctx, accAddr)
	s.Require().True(isJailed, "participant should be marked as jailed")

	// 2. Test un-jailing
	err = hooks.AfterValidatorBonded(s.ctx, nil, valAddr)
	s.Require().NoError(err)

	isJailed = s.k.IsJailed(s.ctx, accAddr)
	s.Require().False(isJailed, "participant should be un-jailed")
}
