package keeper_test

import (
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/collateral/types"
	inftypes "github.com/productscience/inference/x/inference/types"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
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
	s.k.AddUnbondingCollateral(s.ctx, participant1, completedEpoch, unbondingAmount1Coin)
	s.k.AddUnbondingCollateral(s.ctx, participant2, completedEpoch, unbondingAmount2Coin)

	// Another unbonding for a future epoch that should NOT be processed
	futureEpoch := completedEpoch + 1
	futureParticipant, _ := sdk.AccAddressFromBech32(sample.AccAddress())
	unbondingFutureCoin := sdk.NewInt64Coin(inftypes.BaseCoin, 50)
	s.k.AddUnbondingCollateral(s.ctx, futureParticipant, futureEpoch, unbondingFutureCoin)

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
	s.k.AdvanceEpoch(s.ctx, completedEpoch)

	// Verify that the processed unbonding entries are gone
	_, found := s.k.GetUnbondingCollateral(s.ctx, participant1, completedEpoch)
	s.Require().False(found, "processed unbonding entry 1 should be removed")
	_, found = s.k.GetUnbondingCollateral(s.ctx, participant2, completedEpoch)
	s.Require().False(found, "processed unbonding entry 2 should be removed")

	// Verify the future-dated entry is still there
	_, found = s.k.GetUnbondingCollateral(s.ctx, futureParticipant, futureEpoch)
	s.Require().True(found, "future-dated unbonding entry should not be processed")
}
