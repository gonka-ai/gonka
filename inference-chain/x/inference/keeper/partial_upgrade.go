package keeper

import (
	"context"
	"fmt"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/productscience/inference/x/inference/types"
)

// SetPartialUpgrade set a specific partialUpgrade in the store from its index
func (k Keeper) SetPartialUpgrade(ctx context.Context, partialUpgrade types.PartialUpgrade) error {
	// key is the height
	return k.PartialUpgrades.Set(ctx, partialUpgrade.Height, partialUpgrade)
}

// GetPartialUpgrade returns a partialUpgrade from its index
func (k Keeper) GetPartialUpgrade(
	ctx context.Context,
	height uint64,

) (val types.PartialUpgrade, found bool) {
	v, err := k.PartialUpgrades.Get(ctx, height)
	if err != nil {
		return val, false
	}
	return v, true
}

// RemovePartialUpgrade removes a partialUpgrade from the store
func (k Keeper) RemovePartialUpgrade(
	ctx context.Context,
	height uint64,

) {
	_ = k.PartialUpgrades.Remove(ctx, height)
}

// GetAllPartialUpgrade returns all partialUpgrade
func (k Keeper) GetAllPartialUpgrade(ctx context.Context) (list []types.PartialUpgrade) {
	iter, err := k.PartialUpgrades.Iterate(ctx, nil)
	if err != nil {
		return nil
	}
	defer iter.Close()
	values, err := iter.Values()
	if err != nil {
		return nil
	}
	return values
}

// GetUpgradePlan returns the currently scheduled upgrade plan from UpgradeKeeper
func (k Keeper) GetUpgradePlan(ctx context.Context) (upgradetypes.Plan, error) {
	return k.UpgradeKeeper.GetUpgradePlan(ctx)
}

// SetLastUpgradeHeight stores the block height of the most recent upgrade
func (k Keeper) SetLastUpgradeHeight(ctx context.Context, height int64) error {
	return k.LastUpgradeHeight.Set(ctx, height)
}

// GetLastUpgradeHeight returns the block height of the most recent upgrade
func (k Keeper) GetLastUpgradeHeight(ctx context.Context) (int64, bool) {
	height, err := k.LastUpgradeHeight.Get(ctx)
	if err != nil {
		return 0, false
	}
	return height, true
}

// HasUpgradeInWindow checks if any upgrade (partial or full) is scheduled or occurred within windowSize blocks
// Performance: Typical iteration over 0-3 scheduled upgrades, only called during trigger evaluation
func (k Keeper) HasUpgradeInWindow(ctx context.Context, currentHeight int64, windowSize int64) (bool, string, error) {
	// FORWARD CHECK: Iterate ALL scheduled upgrades (must check all since multiple can be scheduled)
	// Check for scheduled full chain upgrade in next windowSize blocks
	upgradePlan, err := k.UpgradeKeeper.GetUpgradePlan(ctx)
	if err == nil && upgradePlan.Height > 0 {
		if upgradePlan.Height >= currentHeight && upgradePlan.Height <= currentHeight+windowSize {
			return true, fmt.Sprintf("full chain upgrade '%s' at height %d", upgradePlan.Name, upgradePlan.Height), nil
		}
	}

	// Check for scheduled partial upgrades in next windowSize blocks
	allUpgrades := k.GetAllPartialUpgrade(ctx)
	for _, upgrade := range allUpgrades {
		if upgrade.Height >= uint64(currentHeight) && upgrade.Height <= uint64(currentHeight+windowSize) {
			return true, fmt.Sprintf("partial upgrade '%s' at height %d", upgrade.Name, upgrade.Height), nil
		}
	}

	// BACKWARD CHECK: Only need MOST RECENT upgrade (if most recent outside window, all earlier ones are too)
	lastUpgradeHeight, found := k.GetLastUpgradeHeight(ctx)
	if found && (currentHeight-lastUpgradeHeight) <= windowSize {
		blocksSince := currentHeight - lastUpgradeHeight
		return true, fmt.Sprintf("upgrade occurred %d blocks ago (at height %d)", blocksSince, lastUpgradeHeight), nil
	}

	return false, "", nil
}
