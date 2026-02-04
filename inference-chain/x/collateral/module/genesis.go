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
			//nolint:forbidigo
			//Genesis code:
			panic(err)
		}
		if err := k.SetCollateral(ctx, participant, elem.Amount); err != nil {
			//nolint:forbidigo
			//Genesis code:
			panic(err)
		}
	}

	// Set all the unbonding collateral entries
	for _, elem := range genState.UnbondingCollateralList {
		participant, err := sdk.AccAddressFromBech32(elem.Participant)
		if err != nil {
			//nolint:forbidigo
			//Genesis code:
			panic(err)
		}
		if err := k.AddUnbondingCollateral(ctx, participant, elem.CompletionEpoch, elem.Amount); err != nil {
			//nolint:forbidigo
			//Genesis code:
			panic(err)
		}
	}

	// Set all the jailedParticipant
	for _, elem := range genState.JailedParticipantList {
		jailedAddr, err := sdk.AccAddressFromBech32(elem.Address)
		if err != nil {
			//nolint:forbidigo
			//Genesis code:
			panic(err)
		}
		if err := k.SetJailed(ctx, jailedAddr); err != nil {
			//nolint:forbidigo
			//Genesis code:
			panic(err)
		}
	}

	// this line is used by starport scaffolding # genesis/module/init
	if err := k.SetParams(ctx, genState.Params); err != nil {
		//nolint:forbidigo
		//Genesis code:
		panic(err)
	}
}

// ExportGenesis returns the module's exported genesis.
func ExportGenesis(ctx sdk.Context, k keeper.Keeper) *types.GenesisState {
	genesis := types.DefaultGenesis()
	params, err := k.GetParams(ctx)
	if err != nil {
		//nolint:forbidigo
		//Genesis code:
		panic(err)
	}
	genesis.Params = params

	collateralBalances := make([]types.CollateralBalance, 0)
	// Export all collateral balances
	if err := k.IterateCollaterals(ctx, func(participant sdk.AccAddress, amount sdk.Coin) (stop bool) {
		collateralBalances = append(collateralBalances, types.CollateralBalance{
			Participant: participant.String(),
			Amount:      amount,
		})
		return false
	}); err != nil {
		//nolint:forbidigo
		//Genesis code:
		panic(err)
	}

	genesis.CollateralBalanceList = collateralBalances

	// Export all unbonding collateral entries
	unbondingCollaterals, err := k.GetAllUnbondings(ctx)
	if err != nil {
		//nolint:forbidigo
		//Genesis code:
		panic(err)
	}
	genesis.UnbondingCollateralList = unbondingCollaterals

	jailedParticipants, err := k.GetAllJailed(ctx)
	if err != nil {
		//nolint:forbidigo
		//Genesis code:
		panic(err)
	}
	genesis.JailedParticipantList = make([]*types.JailedParticipant, len(jailedParticipants))
	for i, addr := range jailedParticipants {
		genesis.JailedParticipantList[i] = &types.JailedParticipant{Address: addr.String()}
	}

	// this line is used by starport scaffolding # genesis/module/export

	return genesis
}
