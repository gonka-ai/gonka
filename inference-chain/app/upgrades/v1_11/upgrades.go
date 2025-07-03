package v1_11

import (
	"context"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"fmt"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"sort"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		for moduleName, version := range vm {
			fmt.Printf("Module: %s, Version: %d\n", moduleName, version)
		}
		fmt.Printf("OrderMigrations: %v\n", mm.OrderMigrations)
		pocStartBlockHeightToEpochId := createEpochs(ctx, k)
		setEpochIdToInferences(ctx, k, pocStartBlockHeightToEpochId)
		updateInferenceValidationDetails(ctx, k)
		return vm, nil
	}
}

func createEpochs(ctx context.Context, k keeper.Keeper) map[uint64]uint64 {
	epochGroupData := k.GetAllEpochGroupData(ctx)
	k.LogInfo(UpgradeName+" - queried all epochGroupData", types.Upgrades, "len(epochGroupData)", len(epochGroupData))
	rootEpochGroups := make([]*types.EpochGroupData, 0)
	for _, epochData := range epochGroupData {
		if epochData.ModelId == "" {
			rootEpochGroups = append(rootEpochGroups, &epochData)
		}
	}
	k.LogInfo(UpgradeName+" - filtered root epoch groups", types.Upgrades, "len(rootEpochGroups)", len(rootEpochGroups))

	sort.Slice(rootEpochGroups, func(i, j int) bool {
		return rootEpochGroups[i].PocStartBlockHeight < rootEpochGroups[j].PocStartBlockHeight
	})

	startBlockHeightToEpochId := make(map[uint64]uint64)
	var lastEpochIndex uint64
	for i, epochGroup := range rootEpochGroups {
		epochId := uint64(i + 1)
		lastEpochIndex = epochId

		k.LogInfo(UpgradeName+" - processing epoch group. "+
			"About to create an epoch and update epochGroupData with EpochId", types.Upgrades,
			"epochGroup.PocStartBlockHeight", epochGroup.PocStartBlockHeight,
			"i", i,
			"epochId", epochId)
		epoch := &types.Epoch{
			Index:               epochId,
			PocStartBlockHeight: int64(epochGroup.PocStartBlockHeight),
		}
		k.SetEpoch(ctx, epoch)

		startBlockHeightToEpochId[epochGroup.PocStartBlockHeight] = epochId

		epochGroup.EpochId = epochId
		k.SetEpochGroupData(ctx, *epochGroup)
	}

	k.LogInfo(UpgradeName+" - created epochs, running SetEffectiveEpochIndex", types.Upgrades, "lastEpochIndex", lastEpochIndex)
	k.SetEffectiveEpochIndex(ctx, lastEpochIndex)

	// TODO: Create genesis epoch
	genesisEpoch := &types.Epoch{
		Index:               0,
		PocStartBlockHeight: 0,
	}
	k.SetEpoch(ctx, genesisEpoch)

	return startBlockHeightToEpochId
}

func setEpochIdToInferences(ctx context.Context, k keeper.Keeper, pocStartBlockHeightToEpochId map[uint64]uint64) {
	// FIXME: add some kind of pagination?
	inferences := k.GetAllInference(ctx)
	k.LogInfo(UpgradeName+" - queried all inferences", types.Upgrades, "len(inference)", len(inferences))
	for _, inference := range inferences {
		epochId, found := pocStartBlockHeightToEpochId[inference.EpochGroupId]
		if !found {
			k.LogError(UpgradeName+" - EpochId not found for Inference", types.Upgrades,
				"inferenceId", inference.InferenceId,
				"epochGroupId", inference.EpochGroupId)
			continue // or handle error
		}
		inference.EpochId = epochId

		// And a field rename:
		inference.EpochPocStartBlockHeight = inference.EpochGroupId

		// TODO: should we retrospectively compute dev stats as well?
		//  does it rely on the current epoch or smth?
		k.SetInferenceWithoutDevStatComputation(ctx, inference)
	}
}

// Basically a field rename
func updateInferenceValidationDetails(ctx context.Context, k keeper.Keeper) {
	vs := k.GetAllInferenceValidationDetails(ctx)
	for _, v := range vs {
		v.EpochGroupId = v.EpochId
	}
}
