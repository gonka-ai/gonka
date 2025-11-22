package keeper_test

import (
	"errors"
	"testing"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestLastUpgradeHeightGetSet(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	// Initially should not be set
	_, found := keeper.GetLastUpgradeHeight(ctx)
	require.False(t, found)

	// Set upgrade height
	err := keeper.SetLastUpgradeHeight(ctx, 1000)
	require.NoError(t, err)

	// Should now be retrievable
	height, found := keeper.GetLastUpgradeHeight(ctx)
	require.True(t, found)
	require.Equal(t, int64(1000), height)

	// Update to new height
	err = keeper.SetLastUpgradeHeight(ctx, 2000)
	require.NoError(t, err)

	// Should have new value
	height, found = keeper.GetLastUpgradeHeight(ctx)
	require.True(t, found)
	require.Equal(t, int64(2000), height)
}

func TestHasUpgradeInWindow_NoUpgrades(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	upgradeKeeper := keepertest.NewMockUpgradeKeeper(ctrl)
	keeper, ctx := keepertest.InferenceKeeperWithUpgradeKeeper(t, upgradeKeeper)

	// No full chain upgrade scheduled
	upgradeKeeper.EXPECT().GetUpgradePlan(gomock.Any()).
		Return(upgradetypes.Plan{}, errors.New("no upgrade plan"))

	// No partial upgrades
	// No last upgrade height set

	hasUpgrade, reason, err := keeper.HasUpgradeInWindow(ctx, 1000, 500)
	require.NoError(t, err)
	require.False(t, hasUpgrade)
	require.Empty(t, reason)
}

func TestHasUpgradeInWindow_FullChainUpgradeInFuture(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	upgradeKeeper := keepertest.NewMockUpgradeKeeper(ctrl)
	keeper, ctx := keepertest.InferenceKeeperWithUpgradeKeeper(t, upgradeKeeper)

	// Full chain upgrade at height 1200 (within 500 blocks of 1000)
	upgradeKeeper.EXPECT().GetUpgradePlan(gomock.Any()).
		Return(upgradetypes.Plan{
			Name:   "v1.0.0",
			Height: 1200,
		}, nil)

	hasUpgrade, reason, err := keeper.HasUpgradeInWindow(ctx, 1000, 500)
	require.NoError(t, err)
	require.True(t, hasUpgrade)
	require.Contains(t, reason, "full chain upgrade")
	require.Contains(t, reason, "v1.0.0")
	require.Contains(t, reason, "1200")
}

func TestHasUpgradeInWindow_FullChainUpgradeOutsideWindow(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	upgradeKeeper := keepertest.NewMockUpgradeKeeper(ctrl)
	keeper, ctx := keepertest.InferenceKeeperWithUpgradeKeeper(t, upgradeKeeper)

	// Full chain upgrade at height 2000 (outside 500 block window from 1000)
	upgradeKeeper.EXPECT().GetUpgradePlan(gomock.Any()).
		Return(upgradetypes.Plan{
			Name:   "v1.0.0",
			Height: 2000,
		}, nil)

	hasUpgrade, reason, err := keeper.HasUpgradeInWindow(ctx, 1000, 500)
	require.NoError(t, err)
	require.False(t, hasUpgrade)
	require.Empty(t, reason)
}

func TestHasUpgradeInWindow_PartialUpgradeInFuture(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	upgradeKeeper := keepertest.NewMockUpgradeKeeper(ctrl)
	keeper, ctx := keepertest.InferenceKeeperWithUpgradeKeeper(t, upgradeKeeper)

	// No full chain upgrade
	upgradeKeeper.EXPECT().GetUpgradePlan(gomock.Any()).
		Return(upgradetypes.Plan{}, nil)

	// Add partial upgrade at height 1300 (within window)
	err := keeper.SetPartialUpgrade(ctx, types.PartialUpgrade{
		Name:   "mlnode-v2",
		Height: 1300,
	})
	require.NoError(t, err)

	hasUpgrade, reason, err := keeper.HasUpgradeInWindow(ctx, 1000, 500)
	require.NoError(t, err)
	require.True(t, hasUpgrade)
	require.Contains(t, reason, "partial upgrade")
	require.Contains(t, reason, "mlnode-v2")
	require.Contains(t, reason, "1300")
}

func TestHasUpgradeInWindow_MultiplePartialUpgrades(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	upgradeKeeper := keepertest.NewMockUpgradeKeeper(ctrl)
	keeper, ctx := keepertest.InferenceKeeperWithUpgradeKeeper(t, upgradeKeeper)

	// No full chain upgrade
	upgradeKeeper.EXPECT().GetUpgradePlan(gomock.Any()).
		Return(upgradetypes.Plan{}, nil)

	// Add multiple partial upgrades
	err := keeper.SetPartialUpgrade(ctx, types.PartialUpgrade{
		Name:   "mlnode-v1",
		Height: 1100,
	})
	require.NoError(t, err)

	err = keeper.SetPartialUpgrade(ctx, types.PartialUpgrade{
		Name:   "mlnode-v2",
		Height: 1200,
	})
	require.NoError(t, err)

	err = keeper.SetPartialUpgrade(ctx, types.PartialUpgrade{
		Name:   "mlnode-v3",
		Height: 2000, // Outside window
	})
	require.NoError(t, err)

	// Should detect first upgrade in window
	hasUpgrade, reason, err := keeper.HasUpgradeInWindow(ctx, 1000, 500)
	require.NoError(t, err)
	require.True(t, hasUpgrade)
	require.Contains(t, reason, "partial upgrade")
	// Could be either v1 or v2 depending on iteration order
	require.True(t, reason == "partial upgrade 'mlnode-v1' at height 1100" ||
		reason == "partial upgrade 'mlnode-v2' at height 1200")
}

func TestHasUpgradeInWindow_RecentUpgradeInPast(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	upgradeKeeper := keepertest.NewMockUpgradeKeeper(ctrl)
	keeper, ctx := keepertest.InferenceKeeperWithUpgradeKeeper(t, upgradeKeeper)

	// No full chain upgrade
	upgradeKeeper.EXPECT().GetUpgradePlan(gomock.Any()).
		Return(upgradetypes.Plan{}, nil)

	// Set last upgrade at height 800 (200 blocks ago from 1000)
	err := keeper.SetLastUpgradeHeight(ctx, 800)
	require.NoError(t, err)

	hasUpgrade, reason, err := keeper.HasUpgradeInWindow(ctx, 1000, 500)
	require.NoError(t, err)
	require.True(t, hasUpgrade)
	require.Contains(t, reason, "upgrade occurred 200 blocks ago")
	require.Contains(t, reason, "800")
}

func TestHasUpgradeInWindow_OldUpgradeOutsideWindow(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	upgradeKeeper := keepertest.NewMockUpgradeKeeper(ctrl)
	keeper, ctx := keepertest.InferenceKeeperWithUpgradeKeeper(t, upgradeKeeper)

	// No full chain upgrade
	upgradeKeeper.EXPECT().GetUpgradePlan(gomock.Any()).
		Return(upgradetypes.Plan{}, nil)

	// Set last upgrade at height 400 (600 blocks ago from 1000, outside 500 window)
	err := keeper.SetLastUpgradeHeight(ctx, 400)
	require.NoError(t, err)

	hasUpgrade, reason, err := keeper.HasUpgradeInWindow(ctx, 1000, 500)
	require.NoError(t, err)
	require.False(t, hasUpgrade)
	require.Empty(t, reason)
}

func TestHasUpgradeInWindow_EdgeCaseExactlyAtWindowBoundary(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	upgradeKeeper := keepertest.NewMockUpgradeKeeper(ctrl)
	keeper, ctx := keepertest.InferenceKeeperWithUpgradeKeeper(t, upgradeKeeper)

	// No full chain upgrade
	upgradeKeeper.EXPECT().GetUpgradePlan(gomock.Any()).
		Return(upgradetypes.Plan{}, nil)

	// Set last upgrade exactly 500 blocks ago (at boundary)
	err := keeper.SetLastUpgradeHeight(ctx, 500)
	require.NoError(t, err)

	hasUpgrade, reason, err := keeper.HasUpgradeInWindow(ctx, 1000, 500)
	require.NoError(t, err)
	require.True(t, hasUpgrade) // Should include boundary
	require.Contains(t, reason, "500")
}

func TestHasUpgradeInWindow_ComplexScenario(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	upgradeKeeper := keepertest.NewMockUpgradeKeeper(ctrl)
	keeper, ctx := keepertest.InferenceKeeperWithUpgradeKeeper(t, upgradeKeeper)

	// Full chain upgrade scheduled in future
	upgradeKeeper.EXPECT().GetUpgradePlan(gomock.Any()).
		Return(upgradetypes.Plan{
			Name:   "v2.0.0",
			Height: 1400,
		}, nil)

	// Multiple partial upgrades
	err := keeper.SetPartialUpgrade(ctx, types.PartialUpgrade{
		Name:   "mlnode-v1",
		Height: 1100,
	})
	require.NoError(t, err)

	err = keeper.SetPartialUpgrade(ctx, types.PartialUpgrade{
		Name:   "mlnode-v2",
		Height: 2000, // Outside window
	})
	require.NoError(t, err)

	// Last upgrade in past
	err = keeper.SetLastUpgradeHeight(ctx, 600)
	require.NoError(t, err)

	// Should detect the full chain upgrade first
	hasUpgrade, reason, err := keeper.HasUpgradeInWindow(ctx, 1000, 500)
	require.NoError(t, err)
	require.True(t, hasUpgrade)
	require.Contains(t, reason, "full chain upgrade")
	require.Contains(t, reason, "v2.0.0")
}
