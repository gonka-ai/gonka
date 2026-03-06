package keeper_test

import (
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/testutil/sample"
	coltypes "github.com/productscience/inference/x/collateral/types"
	inftypes "github.com/productscience/inference/x/inference/types"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
)

// Ensures that duplicate slashing within the same epoch and same reason is prevented
func (s *KeeperTestSuite) TestSlashing_DuplicateGuard_SameReasonSameEpoch() {
	t := s.T()
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)

	// Seed collateral
	s.Require().NoError(s.k.SetCollateral(s.ctx, participant, sdk.NewInt64Coin(inftypes.BaseCoin, 1000)))

	// Expect only one governance redirect, from the first slash
	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToModule(s.ctx, coltypes.ModuleName, govtypes.ModuleName, gomock.Any(), gomock.Any()).
		Times(1)

	frac := math.LegacyNewDecWithPrec(10, 2) // 10%

	// First time with reason invalidation should succeed
	_, err = s.k.Slash(s.ctx, participant, frac, inftypes.SlashReasonInvalidation, math.ZeroInt())
	if err != nil {
		t.Fatalf("first slash failed: %v", err)
	}

	// Second time in same epoch and same reason should error and not burn again
	_, err = s.k.Slash(s.ctx, participant, frac, inftypes.SlashReasonInvalidation, math.ZeroInt())
	if err == nil {
		t.Fatalf("expected error on duplicate slash, got nil")
	}
}

// Ensures that different reasons within the same epoch are allowed
func (s *KeeperTestSuite) TestSlashing_DifferentReasonSameEpoch_Allowed() {
	participantStr := sample.AccAddress()
	participant, err := sdk.AccAddressFromBech32(participantStr)
	s.Require().NoError(err)

	// Seed collateral
	s.Require().NoError(s.k.SetCollateral(s.ctx, participant, sdk.NewInt64Coin(inftypes.BaseCoin, 1000)))

	// Expect two governance redirects: one for each distinct reason
	s.bankKeeper.EXPECT().
		SendCoinsFromModuleToModule(s.ctx, coltypes.ModuleName, govtypes.ModuleName, gomock.Any(), gomock.Any()).
		Times(2)

	frac := math.LegacyNewDecWithPrec(10, 2)

	// First reason: invalidation
	_, err = s.k.Slash(s.ctx, participant, frac, inftypes.SlashReasonInvalidation, math.ZeroInt())
	s.Require().NoError(err)

	// Second reason: downtime (same epoch)
	_, err = s.k.Slash(s.ctx, participant, frac, inftypes.SlashReasonDowntime, math.ZeroInt())
	s.Require().NoError(err)
}
