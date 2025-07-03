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
		return vm, nil
	}
}

func createEpochs(ctx context.Context, k keeper.Keeper) error {
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

	var lastEpochIndex uint64
	for i, epochGroup := range rootEpochGroups {
		lastEpochIndex = uint64(i)
		epoch := &types.Epoch{
			Index:               uint64(i) + 1,
			PocStartBlockHeight: int64(epochGroup.PocStartBlockHeight),
		}
		k.SetEpoch(ctx, epoch)
	}

	k.SetEffectiveEpochIndex(ctx, lastEpochIndex)

	return nil
}
