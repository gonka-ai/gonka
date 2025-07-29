package v1_17

import (
	"context"
	"fmt"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
)

const (
	inferenceValidationCutoff = 50
	setNewValidatorsDelay     = 120
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		{
			for moduleName, version := range vm {
				fmt.Printf("Module: %s, Version: %d\n", moduleName, version)
			}
			fmt.Printf("OrderMigrations: %v\n", mm.OrderMigrations)

			var inferenceModuleFound bool
			for _, name := range mm.ModuleNames() {
				fmt.Printf("Module Name: %s\n", name)
				if name == types.ModuleName {
					inferenceModuleFound = true
					break
				}
			}

			// Mostly for debugging,
			// because we're using this approach with RegisterMigration for the first time
			if !inferenceModuleFound {
				k.LogError("Inference module not found in module manager during upgrade: %v", types.Upgrades)
				return nil, fmt.Errorf("inference module not found in module manager")
			}

			err := configurator.RegisterMigration(types.ModuleName, 3, func(ctx sdk.Context) error {
				SetGenesisModels(ctx, k)
				return SetInferenceCutoffDefault(ctx, k)
			})
			if err != nil {
				k.LogError("Failed to register migration during upgrade: %v", types.Upgrades, "error", err)
				return nil, fmt.Errorf("failed to register migration: %w", err)
			}

			// For some reason, the capability module doesn't have a version set, but it DOES exist, causing
			// the `InitGenesis` to panic.
			if _, ok := vm["capability"]; !ok {
				vm["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
			}

			return mm.RunMigrations(ctx, configurator, vm)
		}
	}
}

func SetGenesisModels(ctx context.Context, k keeper.Keeper) {
	qwen7BModel := types.Model{
		ProposedBy:             "genesis",
		Id:                     "Qwen/Qwen2.5-7B-Instruct",
		UnitsOfComputePerToken: 100,
		HfRepo:                 "Qwen/Qwen2.5-7B-Instruct",
		HfCommit:               "a09a35458c702b33eeacc393d103063234e8bc28",
		ModelArgs:              []string{"--quantization", "fp8"},
		VRam:                   24,
		ThroughputPerNonce:     10000,
	}
	k.SetModel(ctx, &qwen7BModel)
	qwq32BModel := types.Model{
		ProposedBy:             "genesis",
		Id:                     "Qwen/QwQ-32B",
		UnitsOfComputePerToken: 1000,
		HfRepo:                 "Qwen/QwQ-32B",
		HfCommit:               "976055f8c83f394f35dbd3ab09a285a984907bd0",
		ModelArgs:              []string{"--quantization", "fp8", "-kv-cache-dtype", "fp8"},
		VRam:                   80,
		ThroughputPerNonce:     1000,
	}
	k.SetModel(ctx, &qwq32BModel)
}

func SetInferenceCutoffDefault(ctx context.Context, k keeper.Keeper) error {
	params := k.GetParams(ctx)
	params.EpochParams.InferenceValidationCutoff = inferenceValidationCutoff
	params.EpochParams.SetNewValidatorsDelay = setNewValidatorsDelay
	err := k.SetParams(ctx, params)
	if err != nil {
		k.LogError("Failed to set params during upgrade: %v", types.Upgrades, "error", err)
	}
	return err
}
