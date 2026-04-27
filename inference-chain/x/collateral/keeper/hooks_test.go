package keeper_test

import (
	"context"

	testkeeper "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	collateralmodule "github.com/productscience/inference/x/collateral/module"
	"github.com/productscience/inference/x/collateral/types"
	inftypes "github.com/productscience/inference/x/inference/types"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"go.uber.org/mock/gomock"
)

type fixedCollateralKeeper struct {
	amount math.Int
}

func (p fixedCollateralKeeper) GetRequiredCollateralForSlash(_ context.Context, _ sdk.AccAddress) math.Int {
	return p.amount
}

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

	// The hook will trigger our Slash function, which in turn redirects slashed funds to gov.
	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToModule(s.ctx, types.ModuleName, govtypes.ModuleName, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx sdk.Context, senderModule, recipientModule string, amt sdk.Coins, memo string) error {
			s.Require().Equal(types.ModuleName, senderModule)
			s.Require().Equal(govtypes.ModuleName, recipientModule)
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

func (s *KeeperTestSuite) TestStakingHooks_BeforeValidatorSlashed_UsesRequiredCollateral() {
	valAddr, accAddr := sample.AccAddressAndValAddress()
	accAddr, err := sdk.AccAddressFromBech32(accAddr.String())
	s.Require().NoError(err)

	provider := fixedCollateralKeeper{amount: math.NewInt(200)}
	s.k, s.ctx = testkeeper.CollateralKeeperWithMockAndProvider(s.T(), s.bankKeeper, provider)

	initialAmount := int64(1000)
	initialCollateral := sdk.NewInt64Coin(inftypes.BaseCoin, initialAmount)
	s.Require().NoError(s.k.SetCollateral(s.ctx, accAddr, initialCollateral))

	slashFraction := math.LegacyNewDecWithPrec(25, 2) // 25%
	expectedSlashedAmount := math.NewInt(50)          // min(200,1000)*25%

	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToModule(s.ctx, types.ModuleName, govtypes.ModuleName, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx sdk.Context, senderModule, recipientModule string, amt sdk.Coins, memo string) error {
			s.Require().Equal(types.ModuleName, senderModule)
			s.Require().Equal(govtypes.ModuleName, recipientModule)
			s.Require().Equal(expectedSlashedAmount, amt.AmountOf(inftypes.BaseCoin))
			s.Require().Equal("collateral_slashed:", memo)
			return nil
		}).
		Times(1)

	hooks := collateralmodule.NewStakingHooks(s.k)
	err = hooks.BeforeValidatorSlashed(s.ctx, valAddr, slashFraction)
	s.Require().NoError(err)

	finalCollateral, found := s.k.GetCollateral(s.ctx, accAddr)
	s.Require().True(found)
	s.Require().Equal(math.NewInt(950), finalCollateral.Amount)
}
