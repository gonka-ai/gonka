// Package v0_2_17 holds the upgrade handler for the v0.2.17 release.
//
// The handler keeps the capability-version fix, runs module migrations, then
// enables the wasm fee group.
//
// If later work bumps a module ConsensusVersion, it must also register the
// corresponding migration in app/upgrades.go's registerMigrations().
package v0_2_17

import (
	"context"
	"slices"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// wasmMinGasPriceNgonka matches the cosmos group rate installed by v0.2.16.
const wasmMinGasPriceNgonka uint64 = 1

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

		// This binary ships wasmd v0.54.11 and wasmvm v2.2.10, which also selects
		// ibc-go v8.8.0. Wasm ConsensusVersion stays 4 and ibc stays 6, so
		// RunMigrations does not touch that state. Reply call-depth limiting,
		// pin/unpin gas, and stricter ICA ProtoJSON checks take effect here.

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		// After migrations, so a fee-params migration cannot drop the wasm group.
		if err := enableWasmFeeGroup(ctx, k); err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}

// enableWasmFeeGroup turns on the compiled wasm fee group at the cosmos rate.
// Epoch and cosmos groups already on the chain are left in place. A wasm
// price governance already set above zero is kept. Liquidity-pool execute
// bypass stays a separate ante decorator and is not changed here.
func enableWasmFeeGroup(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.FeeParams == nil {
		params.FeeParams = types.DefaultFeeParams()
	}
	fp := params.FeeParams

	group := fp.GroupByName(types.FeeGroupWasm)
	if group == nil {
		group = &types.FeeGroup{Name: types.FeeGroupWasm, MinGasPrice: wasmMinGasPriceNgonka}
		fp.Groups = append(fp.Groups, group)
	} else if group.MinGasPrice == 0 {
		group.MinGasPrice = wasmMinGasPriceNgonka
	}
	if !slices.Contains(fp.EnabledFeeGroups, types.FeeGroupWasm) {
		fp.EnabledFeeGroups = append(fp.EnabledFeeGroups, types.FeeGroupWasm)
	}
	if err := fp.Validate(); err != nil {
		return err
	}

	k.LogInfo("enabled wasm fee group", types.Upgrades,
		"min_gas_price", group.MinGasPrice,
		"enabled_fee_groups", fp.EnabledFeeGroups)
	return k.SetParams(ctx, params)
}
