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
	// Upgrade v0.2.4 & Review, Merged on Epoch # 60
	// Total reward: 24,000 GNK
	// Review is distributed to all PR reviewers proportionally to weight of their nodes and contributions to the PR.
	upgradeV024Bounties = []BountyReward{
		// AnzeKovac, review
		// weight = 6549
		{"gonka1ktl3kkn9l68c9amanu8u4868mcjmtsr5tgzmjk", 1451927577288},

		// scuwan, review
		// weight = 11856
		{"gonka1d7p03cu2y2yt3vytq9wlfm6tlz0lfhlgv9h82p", 2628501046927},
		// weight = 14093
		{"gonka1p2lhgng7tcqju7emk989s5fpdr7k2c3ek6h26m", 3124448823747},
		// weight = 5556
		{"gonka1vhprg9epy683xghp8ddtdlw2y9cycecmm64tje", 1231777312477},
		// weight = 310
		{"gonka15p7s7w2hx0y8095lddd4ummm2y0kwpwljk00aq", 68727675822},

		// blizko, review + potential issue
		// weight = 13370
		{"gonka12jaf7m4eysyqt32mrgarum6z96vt55tckvcleq", 5964158147555},

		// iamoeco
		// weight = 27320
		{"gonka1d9cewcmhq4ez9xgld54qgee06fhk3qy4tqza88", 6056903559552},

		// Pawel-TU
		// weight = 2136
		{"gonka19e5cl3ukk9yjmeza53eapnhwqqytelh77sq6gv", 473555856633},

		// PR Authors (reduced)
		{"gonka18lluv53n4h9z34qu20vxcvypgdkhsg6nn2cl2d", 3000000000000},
	}

	// Upgrade v0.2.5 & Review, Merged on Epoch #89
	// Total reward: 24,000 GNK
	// Review is distributed to all PR reviewers proportionally to weight of their nodes and contributions to the PR.
	upgradeV025Bounties = []BountyReward{
		// AnzeKovac
		// weight = 6523
		{"gonka1ktl3kkn9l68c9amanu8u4868mcjmtsr5tgzmjk", 1181663256064},

		// Pegasus-starry
		// weight = 3098
		{"gonka1d7p03cu2y2yt3vytq9wlfm6tlz0lfhlgv9h82p", 561213056459},
		// weight = 3107
		{"gonka1p2lhgng7tcqju7emk989s5fpdr7k2c3ek6h26m", 562843436546},
		// weight = 12427
		{"gonka1vhprg9epy683xghp8ddtdlw2y9cycecmm64tje", 2251192592841},
		// weight = 357
		{"gonka15p7s7w2hx0y8095lddd4ummm2y0kwpwljk00aq", 64671743433},

		// blizko
		// weight = 27191
		{"gonka12jaf7m4eysyqt32mrgarum6z96vt55tckvcleq", 4925740548157},

		// iamoeco
		// weight = 36964 + 4225 + 25344
		{"gonka1d9cewcmhq4ez9xgld54qgee06fhk3qy4tqza88", 12052675366500},

		// PR Authors (reduced)
		{"gonka18lluv53n4h9z34qu20vxcvypgdkhsg6nn2cl2d", 2400000000000},
	}

	// Bounty Program
	bountyProgramRewards = []BountyReward{
		// Bug Bounty: Vulnerability in Confirmation PoC
		// https://github.com/gonka-ai/gonka/pull/459/commits/b44d51e0cce56f7d8ea35122e1b49cd4be9dd287
		{"gonka1gmuxdcxlsxn5z72elx77w9zym7yrgfxqgzg6ry", 20000000000000},

		// Bug Bounty: Bridge Exchange Double Vote Case Bypass
		{"gonka1s8szs7n43jxgz4a4xaxmzm5emh7fmjxhach7w8", 10000000000000},
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

	// Temporary increase to make sure new payload storage is stable
	params.ValidationParams.BinomTestP0 = types.DecimalFromFloat(0.40)

	params.BandwidthLimitsParams.MaxInferencesPerBlock = 1000

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

	var totalRequired int64
	for _, section := range sections {
		for _, bounty := range section.bounties {
			totalRequired += bounty.Amount
		}
	}

	feePool, err := distrKeeper.FeePool.Get(ctx)
	if err != nil {
		k.Logger().Warn("failed to get fee pool, skipping bounty distribution", "error", err)
		return nil
	}

	available := feePool.CommunityPool.AmountOf(types.BaseCoin).TruncateInt64()
	if available < totalRequired {
		k.Logger().Warn("insufficient fee pool balance, skipping bounty distribution",
			"required", totalRequired, "available", available)
		return nil
	}

	k.Logger().Info("fee pool balance sufficient", "required", totalRequired, "available", available)

	for _, section := range sections {
		for _, bounty := range section.bounties {
			recipient, err := sdk.AccAddressFromBech32(bounty.Address)
			if err != nil {
				k.Logger().Error("invalid bounty address", "address", bounty.Address, "error", err)
				continue
			}

			coins := sdk.NewCoins(sdk.NewCoin(types.BaseCoin, math.NewInt(bounty.Amount)))
			if err := distrKeeper.DistributeFromFeePool(ctx, coins, recipient); err != nil {
				k.Logger().Error("failed to distribute bounty", "address", bounty.Address, "error", err)
				continue
			}

			k.Logger().Info("bounty distributed", "section", section.name, "address", bounty.Address, "amount", bounty.Amount)
		}
	}

	return nil
}
