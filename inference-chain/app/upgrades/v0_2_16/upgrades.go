// Package v0_2_16 holds the upgrade handler scaffold for the v0.2.16 release.
//
// At bootstrap time this stays intentionally small: capability-version fix
// plus RunMigrations. As upgrade work lands, add migration steps below the
// capability fix and above RunMigrations.
//
// If later work bumps a module ConsensusVersion, it must also register the
// corresponding migration in app/upgrades.go's registerMigrations().
package v0_2_16

import (
	"context"
	"encoding/json"
	"fmt"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// UpgradeInfo is extra JSON in the software-upgrade proposal's `info` /
// --upgrade-info field. Cosmovisor already stores binaries/api_binaries in the
// same object; unknown keys are ignored.
//
// Omitted or empty enabled_fee_groups keeps coins off (extra gas still runs).
// Enable later with MsgUpdateParams. To charge at upgrade height, add the same
// field the param uses:
//
//	"enabled_fee_groups": ["epoch"],
//	"min_gas_prices": {"epoch": 10}
type UpgradeInfo struct {
	EnabledFeeGroups []string          `json:"enabled_fee_groups"`
	MinGasPrices     map[string]uint64 `json:"min_gas_prices"`
}

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		// Capability state can already exist even when the version map entry is
		// missing. Set it explicitly so RunMigrations does not re-run InitGenesis.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		// Apply after RunMigrations: inference module 14 forces enabled=[].
		if err := applyFeeGroupUpgradeInfo(ctx, k, plan.Info); err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}

func applyFeeGroupUpgradeInfo(ctx context.Context, k keeper.Keeper, infoJSON string) error {
	if infoJSON == "" {
		k.LogInfo("no upgrade info, fee groups stay disabled", types.Upgrades)
		return nil
	}

	var info UpgradeInfo
	if err := json.Unmarshal([]byte(infoJSON), &info); err != nil {
		return fmt.Errorf("unmarshal v0.2.16 upgrade info: %w", err)
	}
	if len(info.EnabledFeeGroups) == 0 {
		k.LogInfo("enabled_fee_groups empty, fee groups stay disabled", types.Upgrades)
		return nil
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("get inference params: %w", err)
	}
	if params.FeeParams == nil {
		params.FeeParams = types.DefaultFeeParams()
	}

	for _, name := range info.EnabledFeeGroups {
		price, ok := info.MinGasPrices[name]
		if !ok || price == 0 {
			return fmt.Errorf("enabled fee group %q requires min_gas_prices[%q] > 0", name, name)
		}
		group := params.FeeParams.GroupByName(name)
		if group == nil {
			return fmt.Errorf("enabled fee group %q has no groups[] entry", name)
		}
		group.MinGasPrice = price
	}
	for name := range info.MinGasPrices {
		found := false
		for _, enabled := range info.EnabledFeeGroups {
			if enabled == name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("min_gas_prices[%q] is not in enabled_fee_groups", name)
		}
	}

	params.FeeParams.EnabledFeeGroups = info.EnabledFeeGroups
	params.FeeParams.MinGasPriceNgonka = 0
	if err := params.FeeParams.Validate(); err != nil {
		return fmt.Errorf("fee params after applying upgrade info: %w", err)
	}
	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("enabled fee groups from upgrade info", types.Upgrades,
		"enabled_fee_groups", info.EnabledFeeGroups,
		"min_gas_prices", info.MinGasPrices)
	return nil
}
