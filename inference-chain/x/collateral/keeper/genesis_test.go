package keeper_test

import (
	"github.com/productscience/inference/testutil/sample"
	collateralmodule "github.com/productscience/inference/x/collateral/module"
	"github.com/productscience/inference/x/collateral/types"
	inftypes "github.com/productscience/inference/x/inference/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (s *KeeperTestSuite) TestGenesis() {
	participant1 := sample.AccAddress()
	participant2 := sample.AccAddress()
	jailedParticipant := sample.AccAddress()

	genesisState := types.GenesisState{
		Params: types.DefaultParams(),
		CollateralBalanceList: []types.CollateralBalance{
			{Participant: participant1, Amount: sdk.NewInt64Coin(inftypes.BaseCoin, 1000)},
			{Participant: participant2, Amount: sdk.NewInt64Coin(inftypes.BaseCoin, 2000)},
		},
		UnbondingCollateralList: []types.UnbondingCollateral{
			{Participant: participant1, CompletionEpoch: 100, Amount: sdk.NewInt64Coin(inftypes.BaseCoin, 100)},
			{Participant: participant2, CompletionEpoch: 101, Amount: sdk.NewInt64Coin(inftypes.BaseCoin, 200)},
		},
		JailedParticipantList: []*types.JailedParticipant{
			{Address: jailedParticipant},
		},
	}

	// Initialize a keeper with this genesis state
	collateralmodule.InitGenesis(s.ctx, s.k, genesisState)

	// Export the state and verify it matches the original
	exportedGenesis := collateralmodule.ExportGenesis(s.ctx, s.k)
	s.Require().NotNil(exportedGenesis)

	s.Require().Equal(genesisState.Params, exportedGenesis.Params)
	s.Require().ElementsMatch(genesisState.CollateralBalanceList, exportedGenesis.CollateralBalanceList)
	s.Require().ElementsMatch(genesisState.UnbondingCollateralList, exportedGenesis.UnbondingCollateralList)
	s.Require().ElementsMatch(genesisState.JailedParticipantList, exportedGenesis.JailedParticipantList)
}
