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

		if err := applyDevshardApprovedVersions(ctx, k, plan.Info); err != nil {
			return fromVM, fmt.Errorf("apply devshard approved versions: %w", err)
		}

		// Future v0.2.16 migration steps land below this line.

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}

type UpgradeInfo struct {
	ApprovedVersions []*types.DevshardApprovedVersion `json:"approved_versions"`
}

func applyDevshardApprovedVersions(ctx context.Context, k keeper.Keeper, infoJSON string) error {
	if infoJSON == "" {
		k.LogInfo("no upgrade info, skipping devshard approved versions", types.Upgrades)
		return nil
	}

	var info UpgradeInfo
	if err := json.Unmarshal([]byte(infoJSON), &info); err != nil {
		return fmt.Errorf("unmarshal upgrade info: %w", err)
	}
	if len(info.ApprovedVersions) == 0 {
		k.LogInfo("no devshard approved versions in upgrade info, skipping", types.Upgrades)
		return nil
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("get inference params: %w", err)
	}
	if params.DevshardEscrowParams == nil {
		params.DevshardEscrowParams = types.DefaultDevshardEscrowParams()
	}

	next := *params.DevshardEscrowParams
	next.ApprovedVersions = append([]*types.DevshardApprovedVersion(nil), params.DevshardEscrowParams.ApprovedVersions...)
	replacedCount := 0
	for i, version := range info.ApprovedVersions {
		if version == nil {
			return fmt.Errorf("approved_versions[%d] cannot be null", i)
		}
		if recorded, ok := recordedReportsValidated(params.DevshardEscrowParams, version.Name); ok && recorded != version.ReportsValidated {
			k.LogError("upgrade info re-declares reports_validated for an approved devshard version; keeping the recorded policy", types.Upgrades,
				"version", version.Name, "recorded", recorded, "proposed", version.ReportsValidated)
			version.ReportsValidated = recorded
		}
		replaced := false
		for j, existing := range next.ApprovedVersions {
			if existing != nil && existing.Name == version.Name {
				next.ApprovedVersions[j] = version
				replaced = true
				break
			}
		}
		if !replaced {
			next.ApprovedVersions = append(next.ApprovedVersions, version)
		} else {
			replacedCount++
		}
	}
	if err := types.ApplyDevshardVersionPolicies(params.DevshardEscrowParams, &next); err != nil {
		return err
	}
	params.DevshardEscrowParams = &next

	if err := k.SetParams(ctx, params); err != nil {
		return fmt.Errorf("set inference params: %w", err)
	}
	k.LogInfo("set devshard approved versions from upgrade info", types.Upgrades,
		"provided", len(info.ApprovedVersions),
		"appended", len(info.ApprovedVersions)-replacedCount,
		"replaced", replacedCount)
	return nil
}

func recordedReportsValidated(p *types.DevshardEscrowParams, name string) (bool, bool) {
	if p == nil {
		return false, false
	}
	for _, pol := range p.VersionPolicies {
		if pol != nil && pol.Name == name {
			return pol.ReportsValidated, true
		}
	}
	for _, v := range p.ApprovedVersions {
		if v != nil && v.Name == name {
			return v.ReportsValidated, true
		}
	}
	return false, false
}
