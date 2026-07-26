// Package v0_2_15 holds the upgrade handler scaffold for the v0.2.15 release.
//
// At bootstrap time this stays intentionally small: capability-version fix
// plus RunMigrations. As upgrade work lands, add migration steps below the
// capability fix and above RunMigrations.
//
// If later work bumps a module ConsensusVersion, it must also register the
// corresponding migration in app/upgrades.go's registerMigrations().
package v0_2_15

import (
	"context"
	"encoding/json"
	"strconv"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

const BountyCommunitySaleContractAddress = "gonka18pkq9mwxxlmyq7kr5txhm060wemg2s4u94wvsfd9w2kdc0u99d6spk8pz2"
const BountyIbcUsdtDenom = "ibc/115F68FBA220A028C6F6ED08EA0C1A9C8C52798B14FB66E6C89D5D8C06A524D4"

func USDT(amount int64) int64 {
	return amount * 1_000_000
}

type BountyReward struct {
	Address string
	Amount  int64
}

var bountyRewards = []BountyReward{
	// RM: devshard v4 release management.
	// Public name: @akup
	{Address: "gonka1ejkupq3cy6p8xd64ew2wlzveml86ckpzn9dl56", Amount: USDT(22000)},

	// RM: devshard v4 upgrade review.
	// Public name: @Ryanchen911
	{Address: "gonka1zqss46r6jf6dhhyaa777kc2ppvjhn0ufkx4y57", Amount: USDT(1000)},

	// PR #1308.
	// Public name: @redstartechno
	{Address: "gonka105ce4495mj0mwkxqeasgdzqfq5jjrfq32eza5l", Amount: USDT(200)},

	// PR #1283: report and fix of medium severity vulnerability.
	// Public name: @0xMayoor
	{Address: "gonka1s8szs7n43jxgz4a4xaxmzm5emh7fmjxhach7w8", Amount: USDT(3000)},

	// PR #1311: report and fix of low severity vulnerability.
	// Public name: @0xMayoor
	{Address: "gonka1s8szs7n43jxgz4a4xaxmzm5emh7fmjxhach7w8", Amount: USDT(500)},
}

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		// Capability state can already exist even when the version map entry is
		// missing. Set it explicitly so RunMigrations does not re-run InitGenesis.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		// Future v0.2.15 migration steps land below this line.

		if err := distributeBountyRewards(ctx, k); err != nil {
			return nil, err
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}

func distributeBountyRewards(ctx context.Context, k keeper.Keeper) error {
	if len(bountyRewards) == 0 {
		k.Logger().Info("No bounty rewards to distribute")
		return nil
	}

	communitySaleAddr, err := sdk.AccAddressFromBech32(BountyCommunitySaleContractAddress)
	if err != nil {
		k.Logger().Error("invalid hardcoded community sale contract address", "address", BountyCommunitySaleContractAddress, "error", err)
		return nil
	}
	authorityAddr, err := sdk.AccAddressFromBech32(k.GetAuthority())
	if err != nil {
		k.Logger().Error("invalid authority address", "authority", k.GetAuthority(), "error", err)
		return nil
	}

	var totalRequired int64
	for _, bounty := range bountyRewards {
		totalRequired += bounty.Amount
	}

	available := k.BankView.SpendableCoin(ctx, communitySaleAddr, BountyIbcUsdtDenom).Amount.Int64()
	if available < totalRequired {
		k.Logger().Warn("insufficient community sale balance, skipping bounty distribution",
			"required", totalRequired, "available", available, "denom", BountyIbcUsdtDenom)
		return nil
	}

	k.Logger().Info("community sale balance sufficient for bounty distribution",
		"required", totalRequired, "available", available, "denom", BountyIbcUsdtDenom)

	permissionedKeeper := wasmkeeper.NewGovPermissionKeeper(k.GetWasmKeeper())
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, bounty := range bountyRewards {
		recipient, err := sdk.AccAddressFromBech32(bounty.Address)
		if err != nil {
			k.Logger().Error("invalid bounty address", "address", bounty.Address, "error", err)
			continue
		}

		msgBz, err := json.Marshal(map[string]any{
			"withdraw_ibc": map[string]string{
				"denom":     BountyIbcUsdtDenom,
				"amount":    strconv.FormatInt(bounty.Amount, 10),
				"recipient": recipient.String(),
			},
		})
		if err != nil {
			k.Logger().Error("failed to marshal community sale withdraw message", "address", bounty.Address, "error", err)
			continue
		}

		if _, err := permissionedKeeper.Execute(sdkCtx, communitySaleAddr, authorityAddr, msgBz, sdk.NewCoins()); err != nil {
			k.Logger().Error("failed to distribute bounty from community sale contract",
				"address", bounty.Address, "amount", bounty.Amount, "denom", BountyIbcUsdtDenom, "error", err)
			continue
		}

		k.Logger().Info("bounty distributed from community sale contract",
			"address", bounty.Address, "amount", bounty.Amount, "denom", BountyIbcUsdtDenom)
	}

	return nil
}
