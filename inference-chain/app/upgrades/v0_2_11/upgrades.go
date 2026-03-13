package v0_2_11

import (
	"context"
	"encoding/json"
	"errors"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// MigrationData expected in the Plan.Info JSON
type MigrationData struct {
	CommunitySaleAddress string `json:"community_sale_address"`
	NewCodeID            uint64 `json:"new_code_id"`
}

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		err := setParameters(ctx, k)
		if err != nil {
			return nil, err
		}
		err = setPruningState(ctx, k)
		if err != nil {
			return nil, err
		}

		err = setEpochParticipantsSets(ctx, k)
		if err != nil {
			return fromVM, err
		}

		err = k.MigrateEpochGroupValidationsToEntries(ctx)
		if err != nil {
			return fromVM, err
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		// Execute Dynamic Contract Migration from Plan.Info
		if err := executeContractMigration(ctx, k, plan.Info); err != nil {
			k.LogError("contract migration failed", types.Upgrades, "error", err)
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}

func setEpochParticipantsSets(ctx context.Context, k keeper.Keeper) error {
	currentEpochIndex, err := k.EffectiveEpochIndex.Get(ctx)
	if err != nil {
		return err
	}
	if currentEpochIndex < 2 {
		return err
	}
	err = setEpochParticipantsSet(ctx, k, currentEpochIndex)
	if err != nil {
		return err
	}
	err = setEpochParticipantsSet(ctx, k, currentEpochIndex-1)
	if err != nil {
		return err
	}
	return nil
}

// executeContractMigration parses the JSON Plan.Info and triggers Contract Migration.
// It uses `allow_all_trade_tokens` set to true to complete the community-sale migration payload.
func executeContractMigration(ctx context.Context, k keeper.Keeper, infoJSON string) error {
	// Note: For all failures except for actual migration issues
	// we return nil so the chain will continue. Otherwise we just need to
	// fix the (obvious) error and try again.
	if infoJSON == "" {
		k.LogInfo("no migration data found in Plan.Info, skipping contract migration", types.Upgrades)
		return nil
	}

	var data MigrationData
	if err := json.Unmarshal([]byte(infoJSON), &data); err != nil {
		k.LogError("failed to unmarshal Plan.Info", types.Upgrades, "info", infoJSON, "error", err)
		// Log the error and do NOT kill the chain
		return nil
	}

	// Get the governance admin address
	adminAddr, err := sdk.AccAddressFromBech32(k.GetAuthority())
	if err != nil {
		k.LogError("invalid governance address", types.Upgrades, "error", err)
		return nil
	}

	// Make sure both arguments are provided
	if data.CommunitySaleAddress == "" || data.NewCodeID == 0 {
		k.LogInfo("incomplete migration data in Plan.Info, skipping contract migration", types.Upgrades, "info", infoJSON)
		return nil
	}

	contractAddr, err := sdk.AccAddressFromBech32(data.CommunitySaleAddress)
	if err != nil {
		k.LogError("invalid contract address in Plan.Info", types.Upgrades, "address", data.CommunitySaleAddress, "error", err)
		return nil
	}

	// Prepare the CosmWasm Migrate message (enabling all trade tokens natively)
	migrateMsg := []byte(`{"allow_all_trade_tokens":true}`)

	// Perform the actual contract migration via the Wasm Keeper
	permissionedKeeper := wasmkeeper.NewDefaultPermissionKeeper(k.GetWasmKeeper())
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	_, err = permissionedKeeper.Migrate(sdkCtx, contractAddr, adminAddr, data.NewCodeID, migrateMsg)
	if err != nil {
		k.LogError("failed to migrate community sale contract", types.Upgrades, "address", data.CommunitySaleAddress, "new_code_id", data.NewCodeID, "error", err)
		return err // We critically fail the upgrade if migration fails but parameters were provided
	}

	k.LogInfo("successfully migrated community sale contract", types.Upgrades, "address", data.CommunitySaleAddress, "new_code_id", data.NewCodeID)
	return nil
}

// setParameters sets the safety_window parameter to 50.
func setParameters(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("failed to get params during upgrade", types.Upgrades, "error", err)
		return err
	}

	// Impossible, but explicitness is important
	if params.EpochParams == nil || params.ValidationParams == nil {
		k.LogError("params not initialized", types.Upgrades)
		return errors.New("Params not initialized")
	}

	params.EpochParams.ConfirmationPocSafetyWindow = 50

	params.ValidationParams.ClaimValidationEnabled = false
	if err := k.SetParams(ctx, params); err != nil {
		k.LogError("failed to set params with safety window", types.Upgrades, "error", err)
		return err
	}

	k.LogInfo("set safety window", types.Upgrades, "safety_window", params.EpochParams.ConfirmationPocSafetyWindow)
	return nil
}

func setPruningState(ctx context.Context, k keeper.Keeper) error {
	state, err := k.PruningState.Get(ctx)
	if err != nil {
		return err
	}
	state.EpochGroupValidationsPrunedEpoch = 0
	return k.PruningState.Set(ctx, state)
}

func setEpochParticipantsSet(ctx context.Context, k keeper.Keeper, epochIndex uint64) error {
	epochActiveParticipants, found := k.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return types.ErrEpochNotFound
	}
	return k.SetActiveParticipantsCache(ctx, epochActiveParticipants)
}
