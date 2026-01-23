package v0_2_8

import (
	"context"
	"errors"
	"fmt"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
	bk blskeeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.Logger().Info("starting upgrade to " + UpgradeName)

		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		err := burnExtraCommunityCoins(ctx, &k)
		if err != nil {
			k.LogError("Error removing community account", types.Tokenomics, "err", err)
		}

		if err := MigrateBLSData(ctx, k, bk); err != nil {
			k.LogError("Error precomputing slot public keys", types.Tokenomics, "err", err)
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

func burnExtraCommunityCoins(ctx context.Context, k *keeper.Keeper) error {
	// This account and it's coins were inadvertently created during genesis. The coins are NOT
	// part of the economic plan for Gonka. The actual community pool coins will not be impacted.
	const moduleName = "pre_programmed_sale"
	expectedAddr := "gonka1rmac644w5hjsyxfggz6e4empxf02vegkt3ppec"

	actualAddr := k.AccountKeeper.GetModuleAddress(moduleName)
	if actualAddr == nil {
		return fmt.Errorf("module account '%s' does not exist", moduleName)
	}

	actualBech32 := actualAddr.String()
	if actualBech32 != expectedAddr {
		return fmt.Errorf("module account address mismatch: expected %s, got %s", expectedAddr, actualBech32)
	}

	coins := k.BankView.SpendableCoins(ctx, actualAddr)
	if coins.IsZero() {
		k.LogInfo("No coins to burn in 'pre_programmed_sale' account", types.Tokenomics, "coins", coins)
		return nil
	}

	err := k.BankKeeper.BurnCoins(ctx, moduleName, coins, "one-time burn of pre_programmed_sale account")
	if err != nil {
		return fmt.Errorf("failed to burn coins: %w", err)
	}

	k.LogInfo("Successfully burned all coins from 'pre_programmed_sale'", types.Tokenomics, "coins", coins)
	return nil
}

func MigrateBLSData(ctx context.Context, k keeper.Keeper, bk blskeeper.Keeper) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Get the currently effective epoch ID from the inference module.
	// This is the epoch currently responsible for validation and threshold signing.
	epochID, found := k.GetEffectiveEpochIndex(sdkCtx)
	if !found {
		bk.Logger().Info("No effective epoch found during upgrade")
		return nil
	}

	bk.Logger().Info("Checking BLS data migration for current epoch", "epoch_id", epochID)
	epochData, err := bk.GetEpochBLSData(sdkCtx, epochID)
	if err != nil {
		if errors.Is(err, blstypes.ErrEpochBLSDataNotFound) {
			bk.Logger().Info("Epoch BLS data not found", "epoch_id", epochID)
			return nil
		}
		return fmt.Errorf("failed to get epoch %d data: %w", epochID, err)
	}

	if epochData.DkgPhase == blstypes.DKGPhase_DKG_PHASE_COMPLETED || epochData.DkgPhase == blstypes.DKGPhase_DKG_PHASE_SIGNED {
		if len(epochData.SlotPublicKeys) == 0 {
			bk.Logger().Info("Generating precomputed slot public keys for epoch", "epoch_id", epochID)
			slotKeys, err := bk.PrecomputeSlotPublicKeysBlst(&epochData)
			if err != nil {
				return fmt.Errorf("failed to precompute slot keys for epoch %d: %w", epochID, err)
			}
			epochData.SlotPublicKeys = slotKeys
			if err := bk.SetEpochBLSData(sdkCtx, epochData); err != nil {
				return fmt.Errorf("failed to save migrated epoch %d data: %w", epochID, err)
			}
			bk.Logger().Info("Successfully precomputed slot public keys for epoch", "epoch_id", epochID)
		}
	}

	return nil
}
