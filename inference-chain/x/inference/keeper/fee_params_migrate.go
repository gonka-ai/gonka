package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// MigrateFeeParamsToTree preserves legacy StoreCommit gas rates and installs default fee groups.
func (k Keeper) MigrateFeeParamsToTree(ctx context.Context) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}

	if params.FeeParams == nil {
		params.FeeParams = types.DefaultFeeParams()
		k.LogInfo("initialized fee params tree (was nil)", types.Upgrades)
		return k.SetParams(ctx, params)
	}

	fp := params.FeeParams
	defaults := types.DefaultFeeParams()
	fp.EnabledFeeGroups = defaults.EnabledFeeGroups
	fp.MinGasPriceNgonka = 0

	if len(fp.Groups) == 0 {
		def := types.DefaultFeeParams()
		overlayStoreCommitRates(def, fp.BaseValidationGas, fp.GasPerPocCount)
		fp.Groups = def.Groups
	}

	for _, name := range fp.EnabledFeeGroups {
		group := fp.GroupByName(name)
		if group == nil {
			group = defaults.GroupByName(name)
			fp.Groups = append(fp.Groups, group)
		}
		group.MinGasPrice = 1
	}

	if types.ClampFeeTreeSafetyLimits(fp) {
		k.LogInfo("clamped legacy fee rates to safety limits", types.Upgrades,
			"max_period_base_gas", types.MaxPeriodBaseGas,
			"max_gas_per_unit", types.MaxGasPerUnit)
	}

	params.FeeParams = fp
	k.LogInfo("migrated fee params to group tree", types.Upgrades,
		"min_gas_price_ngonka", fp.MinGasPriceNgonka,
		"enabled_fee_groups", fp.EnabledFeeGroups,
		"groups", len(fp.Groups))
	return k.SetParams(ctx, params)
}

func overlayStoreCommitRates(fp *types.FeeParams, baseGas, gasPerUnit uint64) {
	storeCommitURL := sdk.MsgTypeURL(&types.MsgPoCV2StoreCommit{})
	for _, g := range fp.Groups {
		if g == nil {
			continue
		}
		for _, rule := range g.Msgs {
			if rule == nil || rule.TypeUrl != storeCommitURL {
				continue
			}
			if rule.Base == nil {
				rule.Base = &types.PeriodBase{PeriodType: types.PeriodTypePoc, PeriodLength: 1}
			}
			rule.Base.Gas = baseGas
			if delta := rule.GetStoredDelta(); delta != nil {
				delta.GasPerUnit = gasPerUnit
			}
		}
	}
}
