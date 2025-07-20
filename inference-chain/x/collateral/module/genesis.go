package collateral

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/collateral/keeper"
	"github.com/productscience/inference/x/collateral/types"
)

// InitGenesis initializes the module's state from a provided genesis state.
func InitGenesis(ctx sdk.Context, k keeper.Keeper, genState types.GenesisState) {
	// Set all the collateral balances
	for _, elem := range genState.CollateralBalanceList {
		participant, err := sdk.AccAddressFromBech32(elem.Participant)
		if err != nil {
			panic(err)
		}
		k.SetCollateral(ctx, participant, elem.Amount)
	}

	// Set all the unbonding collateral entries
	for _, elem := range genState.UnbondingCollateralList {
		participant, err := sdk.AccAddressFromBech32(elem.Participant)
		if err != nil {
			panic(err)
		}
		k.AddUnbondingCollateral(ctx, participant, elem.CompletionEpoch, elem.Amount)
	}

	// Set all the jailedParticipant
	for _, elem := range genState.JailedParticipantList {
		jailedAddr, err := sdk.AccAddressFromBech32(elem.Address)
		if err != nil {
			panic(err)
		}
		k.SetJailed(ctx, jailedAddr)
	}

	// this line is used by starport scaffolding # genesis/module/init
	if err := k.SetParams(ctx, genState.Params); err != nil {
		panic(err)
	}
}

// ExportGenesis returns the module's exported genesis.
func ExportGenesis(ctx sdk.Context, k keeper.Keeper) *types.GenesisState {
	genesis := types.DefaultGenesis()
	genesis.Params = k.GetParams(ctx)

	// Export all collateral balances
	collateralMap := k.GetAllCollaterals(ctx)
	collateralBalances := make([]types.CollateralBalance, 0, len(collateralMap))

	for participant, amount := range collateralMap {
		collateralBalances = append(collateralBalances, types.CollateralBalance{
			Participant: participant,
			Amount:      amount,
		})
	}

	genesis.CollateralBalanceList = collateralBalances

	// Export all unbonding collateral entries
	unbondingCollaterals := k.GetAllUnbondings(ctx)
	genesis.UnbondingCollateralList = unbondingCollaterals

	jailedParticipants := k.GetAllJailed(ctx)
	genesis.JailedParticipantList = make([]*types.JailedParticipant, len(jailedParticipants))
	for i, addr := range jailedParticipants {
		genesis.JailedParticipantList[i] = &types.JailedParticipant{Address: addr}
	}

	// this line is used by starport scaffolding # genesis/module/export

	return genesis
}
