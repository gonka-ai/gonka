package v0_2_6

import (
	"context"

	"cosmossdk.io/math"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

type BountyReward struct {
	Address string
	Amount  int64
}

var (
	// Upgrade v0.2.4 & Review
	upgradeV024Bounties = []BountyReward{
		// {"gonka1...", 1000000000000000},
	}

	// Upgrade v0.2.5 & Review
	upgradeV025Bounties = []BountyReward{
		// {"gonka1...", 1000000000000000},
	}

	// Bounty Program
	bountyProgramRewards = []BountyReward{
		// {"gonka1...", 1000000000000000},
	}
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
	distrKeeper distrkeeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.Logger().Info("starting upgrade to " + UpgradeName)

		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		if err := setNewPocParams(ctx, k); err != nil {
			return nil, err
		}

		if err := distributeBountyRewards(ctx, k, distrKeeper); err != nil {
			return nil, err
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.Logger().Info("successfully upgraded to " + UpgradeName)
		return toVM, nil
	}
}

func setNewPocParams(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParamsSafe(ctx)
	if err != nil {
		return err
	}

	if params.PocParams == nil {
		params.PocParams = types.DefaultPocParams()
	}
	params.PocParams.WeightScaleFactor = types.DecimalFromFloat(2.5)
	params.PocParams.ModelParams = types.DefaultPoCModelParams()
	params.PocParams.ModelParams.RTarget = types.DecimalFromFloat(1.398077)

	params.ValidationParams.ExpirationBlocks = 150

	params.BandwidthLimitsParams.MaxInferencesPerBlock = 100

	return k.SetParams(ctx, params)
}

func distributeBountyRewards(ctx context.Context, k keeper.Keeper, distrKeeper distrkeeper.Keeper) error {
	sections := []struct {
		name     string
		bounties []BountyReward
	}{
		{"upgrade_v0.2.4_review", upgradeV024Bounties},
		{"upgrade_v0.2.5_review", upgradeV025Bounties},
		{"bounty_program", bountyProgramRewards},
	}

	for _, section := range sections {
		for _, bounty := range section.bounties {
			recipient, err := sdk.AccAddressFromBech32(bounty.Address)
			if err != nil {
				return err
			}

			coins := sdk.NewCoins(sdk.NewCoin(types.BaseCoin, math.NewInt(bounty.Amount)))
			if err := distrKeeper.DistributeFromFeePool(ctx, coins, recipient); err != nil {
				return err
			}

			k.Logger().Info("bounty distributed", "section", section.name, "address", bounty.Address, "amount", bounty.Amount)
		}
	}

	return nil
}
