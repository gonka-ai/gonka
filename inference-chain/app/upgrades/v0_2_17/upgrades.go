// Package v0_2_17 prepares the transitional PREFILL and permanent DECODE recipes,
// writes the epoch-validation admission caps, and enables the wasm fee group.
//
// Moves flat recipes and dynamic coefficients into PREFILL blocks, adds DECODE
// templates, and preserves poc_scheme. Pre-v0.2.16 static weight factors remain
// on the model. Admission defaults are written when stored params do not have
// them yet, then retained accounts and weights are repaired. After migrations,
// the wasm fee group is enabled, the epoch fee group carries the new
// store-commit key price, and the onboarding fee group is enabled.
package v0_2_17

import (
	"context"
	"fmt"
	"slices"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
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
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		// Existing chains have no epoch_validation_params field. Write the
		// admission defaults before the audit so this block reads the governed caps.
		if err := migrateEpochValidationParams(ctx, k); err != nil {
			return nil, err
		}

		// Capability state can already exist even when the version map entry is
		// missing. Set it explicitly so RunMigrations does not re-run InitGenesis.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		// Canonical-account validation finishes before any audit write. Distribution
		// and epoch-group rewrites then run before this block's epoch stage.
		if err := k.RepairEpochValidationState(ctx); err != nil {
			return nil, err
		}

		if err := migratePocSchemeBlocks(ctx, k); err != nil {
			return fromVM, err
		}
		if err := freezeActiveEpochRecipe(ctx, k); err != nil {
			return fromVM, err
		}

		// Index records left pending before the recheck existed; EndBlock re-evaluates them.
		if n, err := k.BackfillBridgePendingIndex(ctx); err != nil {
			k.LogError("bridge pending backfill failed", types.Upgrades, "error", err)
		} else {
			k.LogInfo("bridge pending backfill", types.Upgrades, "records", n)
		}

		// This binary ships wasmd v0.54.11 and wasmvm v2.2.10, which also selects
		// ibc-go v8.8.0. Wasm ConsensusVersion stays 4 and ibc stays 6, so
		// RunMigrations does not touch that state. Reply call-depth limiting,
		// pin/unpin gas, and stricter ICA ProtoJSON checks take effect here.
		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		// After migrations, so a fee-params migration cannot drop these groups.
		// The signer/stage validation cap is gone in this binary, so the epoch
		// fee group must carry the new store-commit key price in the same upgrade.
		if err := enableWasmFeeGroup(ctx, k); err != nil {
			return toVM, err
		}
		if err := updateEpochFeeGroup(ctx, k); err != nil {
			return toVM, err
		}
		if err := enableOnboardingFeeGroup(ctx, k); err != nil {
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

// updateEpochFeeGroup writes the activation price for a newly inserted
// (stage, participant, model) store-commit key on the epoch fee group.
// The price is a static rule. stored_delta stays the count rate. The
// signer/stage validation cap is not enforced by this binary, so this
// upgrade must not finish while new keys are still free.
func updateEpochFeeGroup(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.FeeParams == nil {
		params.FeeParams = types.DefaultFeeParams()
	}
	fp := params.FeeParams
	group := fp.GroupByName(types.FeeGroupEpoch)
	if group == nil {
		return fmt.Errorf("epoch fee group missing; refusing to activate without a new-key charge")
	}
	storeURL := sdk.MsgTypeURL(&types.MsgPoCV2StoreCommit{})
	var delta, staticRule *types.MsgGasRule
	for _, rule := range group.Msgs {
		if rule == nil || rule.TypeUrl != storeURL {
			continue
		}
		if rule.GetStoredDelta() != nil {
			delta = rule
		}
		if rule.GetStatic() != nil {
			staticRule = rule
		}
	}
	if delta == nil {
		return fmt.Errorf("store commit fee rule missing; refusing to activate without a new-key charge")
	}
	if staticRule == nil {
		staticRule = &types.MsgGasRule{TypeUrl: storeURL}
		group.Msgs = append(group.Msgs, staticRule)
	}
	staticRule.Base = nil
	staticRule.Func = &types.MsgGasRule_Static{
		Static: &types.StaticParams{Gas: types.StoreCommitGasPerNewKey},
	}
	if err := fp.Validate(); err != nil {
		return err
	}
	k.LogInfo("updated epoch fee group", types.Upgrades,
		"static_gas", staticRule.GetStatic().GetGas(),
		"ngonka_per_key_at_price_1", staticRule.GetStatic().GetGas())
	return k.SetParams(ctx, params)
}

// enableOnboardingFeeGroup turns on the onboarding fee group at 1 ngonka
// per gas and sets the one-time creation surcharge. Updating an existing
// participant does not pay that surcharge. This does not change the epoch
// fee group's StoreCommit period base or static new-key gas.
func enableOnboardingFeeGroup(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.FeeParams == nil {
		params.FeeParams = types.DefaultFeeParams()
	}
	fp := params.FeeParams
	group := fp.GroupByName(types.FeeGroupOnboarding)
	if group == nil {
		group = &types.FeeGroup{Name: types.FeeGroupOnboarding, MinGasPrice: wasmMinGasPriceNgonka}
		fp.Groups = append(fp.Groups, group)
	} else if group.MinGasPrice == 0 {
		group.MinGasPrice = wasmMinGasPriceNgonka
	}
	submitURL := sdk.MsgTypeURL(&types.MsgSubmitNewParticipant{})
	var rule *types.MsgGasRule
	for _, candidate := range group.Msgs {
		if candidate != nil && candidate.TypeUrl == submitURL {
			rule = candidate
			break
		}
	}
	if rule == nil {
		rule = &types.MsgGasRule{TypeUrl: submitURL}
		group.Msgs = append(group.Msgs, rule)
	}
	rule.Func = &types.MsgGasRule_Static{
		Static: &types.StaticParams{Gas: types.NewParticipantGas},
	}
	if !slices.Contains(fp.EnabledFeeGroups, types.FeeGroupOnboarding) {
		fp.EnabledFeeGroups = append(fp.EnabledFeeGroups, types.FeeGroupOnboarding)
	}
	if err := fp.Validate(); err != nil {
		return err
	}
	k.LogInfo("enabled onboarding fee group", types.Upgrades,
		"min_gas_price", group.MinGasPrice,
		"static_gas", rule.GetStatic().GetGas(),
		"enabled_fee_groups", fp.EnabledFeeGroups)
	return k.SetParams(ctx, params)
}

// migrateEpochValidationParams writes the admission caps when the stored
// params do not have them yet. A later governance value is left in place.
func migrateEpochValidationParams(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.EpochValidationParams != nil {
		return nil
	}
	params.EpochValidationParams = types.DefaultEpochValidationParams()
	return k.SetParams(ctx, params)
}

func freezeActiveEpochRecipe(ctx context.Context, k keeper.Keeper) error {
	epoch, found := k.GetEffectiveEpoch(ctx)
	if !found || epoch == nil {
		return fmt.Errorf("cannot freeze upgrade recipe: effective epoch not found")
	}
	height := epoch.PocStartBlockHeight
	if participants, ok := k.GetActiveParticipants(ctx, epoch.Index); ok {
		height = participants.PocStartBlockHeight
	}
	_, err := k.FreezePocStageRecipe(ctx, height)
	return err
}

// migratePocSchemeBlocks moves flat fields into PREFILL blocks and clears them.
// The upgrade fails if any model cannot be converted.
func migratePocSchemeBlocks(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.PocParams == nil {
		return nil
	}
	for _, model := range params.PocParams.Models {
		if model == nil {
			return fmt.Errorf("poc scheme migration: models contains a nil entry")
		}
		if model.SeqLen <= 0 {
			return fmt.Errorf("model %q has invalid prefill seq_len %d", model.ModelId, model.SeqLen)
		}
		model.Schemes = []*types.PocSchemeParams{
			{
				Scheme:             types.PocScheme_POC_SCHEME_PREFILL,
				SeqLen:             model.SeqLen,
				StatTest:           pinFraudAlpha(model.StatTest),
				DynamicCoefficient: model.DynamicCoefficient,
			},
			decodeScheme(model.ModelId),
		}
		model.SeqLen = 0
		model.StatTest = nil
		model.DynamicCoefficient = nil
	}
	if err := params.Validate(); err != nil {
		return fmt.Errorf("poc scheme migration produced invalid params: %w", err)
	}
	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("migrated prefill scheme blocks", types.Upgrades, "models", len(params.PocParams.Models))
	return nil
}
