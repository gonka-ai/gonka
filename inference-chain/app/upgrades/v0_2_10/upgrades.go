package v0_2_10

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

func Gonka(amount int64) int64 {
	return amount * 1_000_000_000
}

type BountyReward struct {
	Address string
	Amount  int64
}

var bountyRewards = []BountyReward{
	// Valid fix for minor vulnerability that was previously reported in issue #422
	// PR: https://github.com/gonka-ai/gonka/pull/661
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: Gonka(500)},

	// Planned task, not a vulnerability, important for the network.
	// PR: https://github.com/gonka-ai/gonka/pull/644
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: Gonka(700)},

	// Detailed report and fix for a Medium risk vulnerability.
	// PR: https://github.com/gonka-ai/gonka/pull/659
	{Address: "gonka1ejkupq3cy6p8xd64ew2wlzveml86ckpzn9dl56", Amount: Gonka(10000)},

	// First report of the vulnerability fixed in #659
	{Address: "gonka1c34w3r45f0uftjckt2yy4k22vnc3zqjnp0umyz", Amount: Gonka(5000)},

	// Report and fix of low risk vulnerability. Extra appreciation for discovering and
	// reporting it during the review of another PR.
	// PR: https://github.com/gonka-ai/gonka/pull/545
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: Gonka(1000)},

	// Valid minor bug fix.
	// PR: https://github.com/gonka-ai/gonka/pull/640
	{Address: "gonka1jkydytz99gkh0t42gjj4lz0mmdeumqp7mtzke3", Amount: Gonka(100)},

	// First report and suggested fix. Fixed in PR #661
	// Issue: https://github.com/gonka-ai/gonka/issues/422
	{Address: "gonka123khww9elhtj49zumz0daleaudl6jn9y87tf23", Amount: Gonka(500)},

	// Valid minor bug fix.
	// PR: https://github.com/gonka-ai/gonka/pull/638
	{Address: "gonka1jkydytz99gkh0t42gjj4lz0mmdeumqp7mtzke3", Amount: Gonka(100)},

	// Valid minor bug fix.
	// PR: https://github.com/gonka-ai/gonka/pull/634
	{Address: "gonka1jkydytz99gkh0t42gjj4lz0mmdeumqp7mtzke3", Amount: Gonka(100)},

	// Independent report on the issue addressed by PR #710.
	{Address: "gonka1f0elpwnx7ezytdlck35003nz6qk8kzvurvnj4a", Amount: Gonka(5000)},

	// Report and fix of low risk vulnerability.
	// PR: https://github.com/gonka-ai/gonka/pull/643
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: Gonka(500)},

	// Valid implementation of a planned task.
	// PR: https://github.com/gonka-ai/gonka/pull/641
	{Address: "gonka1jkydytz99gkh0t42gjj4lz0mmdeumqp7mtzke3", Amount: Gonka(1500)},

	// Valid minor vulnerability report and fix.
	// PR: https://github.com/gonka-ai/gonka/pull/622
	{Address: "gonka1s8szs7n43jxgz4a4xaxmzm5emh7fmjxhach7w8", Amount: Gonka(700)},

	// Valid implementation of a planned task with adjusting scope, important for the network.
	// PR: https://github.com/gonka-ai/gonka/pull/688
	{Address: "gonka1s8szs7n43jxgz4a4xaxmzm5emh7fmjxhach7w8", Amount: Gonka(1500)},

	// PR review of upgrade v.0.2.8.
	{Address: "gonka12jaf7m4eysyqt32mrgarum6z96vt55tckvcleq", Amount: Gonka(2500)},

	// PR review of upgrade v.0.2.8.
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: Gonka(2500)},

	// PR review of upgrade v.0.2.8.
	{Address: "gonka1f0elpwnx7ezytdlck35003nz6qk8kzvurvnj4a", Amount: Gonka(2500)},

	// PR review of upgrade v.0.2.8.
	{Address: "gonka1ejkupq3cy6p8xd64ew2wlzveml86ckpzn9dl56", Amount: Gonka(2500)},

	// PR review of upgrade v.0.2.8.
	{Address: "gonka1zqss46r6jf6dhhyaa777kc2ppvjhn0ufkx4y57", Amount: Gonka(2500)},

	// PR review of upgrade v.0.2.9.
	{Address: "gonka12jaf7m4eysyqt32mrgarum6z96vt55tckvcleq", Amount: Gonka(2500)},

	// PR review of upgrade v.0.2.9.
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: Gonka(2500)},
}

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

		setValidationSlots(ctx, k)
		setPocNormalizationEnabled(ctx, k)
		setPocTimingParams(ctx, k)
		updateQwenModel(ctx, k)
		updateCurrentEpochModelSnapshot(ctx, k)
		addPunishmentGraceEpoch(ctx, k)

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

// setValidationSlots explicitly sets ValidationSlots to 0 (disabled).
// This keeps O(N^2) validation behavior until sampling is enabled via governance.
// Must be enabled only when new participant cost > 0 (see proposals/poc/optimize.md).
func setValidationSlots(ctx context.Context, k keeper.Keeper) {
	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("failed to get params during upgrade", types.Upgrades, "error", err)
		return
	}

	if params.PocParams == nil {
		k.LogError("poc params not initialized", types.Upgrades)
		return
	}

	params.PocParams.ValidationSlots = 0

	if err := k.SetParams(ctx, params); err != nil {
		k.LogError("failed to set params with validation slots", types.Upgrades, "error", err)
		return
	}

	k.LogInfo("set validation slots", types.Upgrades, "validation_slots", params.PocParams.ValidationSlots)
}

// setPocNormalizationEnabled explicitly enables time-based weight normalization for PoC.
func setPocNormalizationEnabled(ctx context.Context, k keeper.Keeper) {
	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("failed to get params during upgrade", types.Upgrades, "error", err)
		return
	}

	if params.PocParams == nil {
		k.LogError("poc params not initialized", types.Upgrades)
		return
	}

	params.PocParams.PocNormalizationEnabled = true

	if err := k.SetParams(ctx, params); err != nil {
		k.LogError("failed to set params with poc normalization enabled", types.Upgrades, "error", err)
		return
	}

	k.LogInfo("set poc normalization enabled", types.Upgrades, "poc_normalization_enabled", params.PocParams.PocNormalizationEnabled)
}

// setPocTimingParams updates PoC timing parameters:
// - Reduces poc_stage_duration from 60 (with 48 effective blocks) to 35 blocks
// - Reduces poc_validation_duration from 480 to 240 blocks
// - Scales weight_scale_factor proportionally from 0.262 to 0.3593 to maintain same total weight
// - Sets poc_exchange_duration to 0 (deprecated, acceptance now ends at poc_generation_end)
func setPocTimingParams(ctx context.Context, k keeper.Keeper) {
	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("failed to get params during upgrade", types.Upgrades, "error", err)
		return
	}

	if params.EpochParams == nil {
		k.LogError("epoch params not initialized", types.Upgrades)
		return
	}
	if params.PocParams == nil {
		k.LogError("poc params not initialized", types.Upgrades)
		return
	}

	// Update PoC timing: reduce from 60 (with 48 effective blocks) to 35 blocks
	params.EpochParams.PocStageDuration = 35
	// Update validation duration: reduce from 480 to 240 blocks
	params.EpochParams.PocValidationDuration = 240
	// Deprecated: set to 0, nonce acceptance now ends at poc_generation_end
	params.EpochParams.PocExchangeDuration = 0
	// Scale weight factor proportionally: 0.262 * (48/35) ≈ 0.3593
	// Keeps total weight accumulation the same: 0.3593 * 35 ≈ 0.262 * 48
	params.PocParams.WeightScaleFactor = &types.Decimal{Value: 3593, Exponent: -4}

	if err := k.SetParams(ctx, params); err != nil {
		k.LogError("failed to set poc timing params", types.Upgrades, "error", err)
		return
	}

	k.LogInfo("set poc timing params", types.Upgrades,
		"poc_stage_duration", params.EpochParams.PocStageDuration,
		"poc_validation_duration", params.EpochParams.PocValidationDuration,
		"poc_exchange_duration", params.EpochParams.PocExchangeDuration,
		"weight_scale_factor", 0.449)
}

// updateQwenModel updates the Qwen model with tool calling arguments and increased threshold.
// Adds --enable-auto-tool-choice and --tool-call-parser hermes for tool calling support.
// Updates validation threshold from 0.970917 to 0.958.
func updateQwenModel(ctx context.Context, k keeper.Keeper) {
	modelID := "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"

	model, found := k.GetGovernanceModel(ctx, modelID)
	if !found {
		k.LogError("model not found during upgrade", types.Upgrades, "model_id", modelID)
		return
	}

	// Add tool calling arguments
	model.ModelArgs = []string{
		"--max-model-len", "240000",
		"--enable-auto-tool-choice",
		"--tool-call-parser", "hermes",
	}

	// Update validation threshold from 0.970917 to 0.958
	model.ValidationThreshold = &types.Decimal{Value: 958, Exponent: -3}

	k.SetModel(ctx, model)

	k.LogInfo("updated model", types.Upgrades,
		"model_id", modelID,
		"model_args", model.ModelArgs,
		"validation_threshold", 0.958)
}

// updateCurrentEpochModelSnapshot updates the ModelSnapshot in the current epoch's EpochGroupData.
// This ensures API nodes get the new model args immediately without waiting for the next epoch.
// The governance model update (updateQwenModel) handles future epochs, while this function
// updates the already-frozen snapshot for the current epoch.
func updateCurrentEpochModelSnapshot(ctx context.Context, k keeper.Keeper) {
	modelID := "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"

	// Get current epoch index
	currentEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogError("no current epoch found during snapshot update", types.Upgrades)
		return
	}

	// Get the model subgroup's EpochGroupData
	epochGroupData, found := k.GetEpochGroupData(ctx, currentEpochIndex, modelID)
	if !found {
		k.LogError("no epoch group data found for model", types.Upgrades,
			"epoch_index", currentEpochIndex,
			"model_id", modelID)
		return
	}

	// Update the ModelSnapshot with new args
	if epochGroupData.ModelSnapshot == nil {
		k.LogError("model snapshot is nil in epoch group data", types.Upgrades,
			"epoch_index", currentEpochIndex,
			"model_id", modelID)
		return
	}

	epochGroupData.ModelSnapshot.ModelArgs = []string{
		"--max-model-len", "240000",
		"--enable-auto-tool-choice",
		"--tool-call-parser", "hermes",
	}
	epochGroupData.ModelSnapshot.ValidationThreshold = &types.Decimal{Value: 958, Exponent: -3}

	// Save updated epoch group data
	k.SetEpochGroupData(ctx, epochGroupData)

	k.LogInfo("updated model snapshot in current epoch", types.Upgrades,
		"epoch_index", currentEpochIndex,
		"model_id", modelID,
		"model_args", epochGroupData.ModelSnapshot.ModelArgs,
		"validation_threshold", 0.958)
}

func addPunishmentGraceEpoch(ctx context.Context, k keeper.Keeper) {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogError("no current epoch found", types.Upgrades)
		return
	}

	binomTestP0 := &types.Decimal{Value: 5, Exponent: -1} // 0.5
	if err := k.AddPunishmentGraceEpoch(ctx, epochIndex, binomTestP0, 3000); err != nil {
		k.LogError("failed to add grace epoch", types.Upgrades, "error", err)
		return
	}
	k.LogInfo("added grace epoch", types.Upgrades, "epoch", epochIndex)
}

func distributeBountyRewards(ctx context.Context, k keeper.Keeper, distrKeeper distrkeeper.Keeper) error {
	if len(bountyRewards) == 0 {
		k.Logger().Info("No bounty rewards to distribute")
		return nil
	}

	var totalRequired int64
	for _, bounty := range bountyRewards {
		totalRequired += bounty.Amount
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

	for _, bounty := range bountyRewards {
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

		k.Logger().Info("bounty distributed", "address", bounty.Address, "amount", bounty.Amount)
	}

	return nil
}
