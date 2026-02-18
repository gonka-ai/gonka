package keeper_test

import (
	"testing"

	keeper2 "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestPunishmentGraceEpoch_AddAndGet(t *testing.T) {
	keeper, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)

	epochIndex := uint64(170)

	_, found := keeper.GetPunishmentGraceEpoch(ctx, epochIndex)
	require.False(t, found)

	binomTestP0 := &types.Decimal{Value: 5, Exponent: -1}
	err := keeper.AddPunishmentGraceEpoch(ctx, epochIndex, binomTestP0, 3000)
	require.NoError(t, err)

	params, found := keeper.GetPunishmentGraceEpoch(ctx, epochIndex)
	require.True(t, found)
	require.Equal(t, epochIndex, params.EpochIndex)
	require.Equal(t, int64(5), params.BinomTestP0.Value)
	require.Equal(t, int32(-1), params.BinomTestP0.Exponent)
	require.Equal(t, int64(3000), params.UpgradeProtectionWindow)
}

func TestPunishmentGraceEpoch_NotFound(t *testing.T) {
	keeper, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)

	_, found := keeper.GetPunishmentGraceEpoch(ctx, 999)
	require.False(t, found)
}

func TestPunishmentGraceEpoch_MultipleEpochs(t *testing.T) {
	keeper, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)

	require.NoError(t, keeper.AddPunishmentGraceEpoch(ctx, 170, &types.Decimal{Value: 5, Exponent: -1}, 3000))
	require.NoError(t, keeper.AddPunishmentGraceEpoch(ctx, 171, &types.Decimal{Value: 4, Exponent: -1}, 2000))

	params170, found := keeper.GetPunishmentGraceEpoch(ctx, 170)
	require.True(t, found)
	require.Equal(t, int64(3000), params170.UpgradeProtectionWindow)

	params171, found := keeper.GetPunishmentGraceEpoch(ctx, 171)
	require.True(t, found)
	require.Equal(t, int64(2000), params171.UpgradeProtectionWindow)

	_, found = keeper.GetPunishmentGraceEpoch(ctx, 172)
	require.False(t, found)
}

func TestPunishmentGraceEpoch_NilBinomTestP0(t *testing.T) {
	keeper, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)

	require.NoError(t, keeper.AddPunishmentGraceEpoch(ctx, 170, nil, 3000))

	params, found := keeper.GetPunishmentGraceEpoch(ctx, 170)
	require.True(t, found)
	require.Nil(t, params.BinomTestP0)
	require.Equal(t, int64(3000), params.UpgradeProtectionWindow)
}

func TestPunishmentGraceEpoch_UpgradeProtectionWindowUsage(t *testing.T) {
	keeper, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)

	epochIndex := uint64(170)
	defaultWindow := int64(500)
	graceWindow := int64(3000)

	// Without grace epoch
	upgradeProtectionWindow := defaultWindow
	if graceParams, ok := keeper.GetPunishmentGraceEpoch(ctx, epochIndex); ok && graceParams.UpgradeProtectionWindow > 0 {
		upgradeProtectionWindow = graceParams.UpgradeProtectionWindow
	}
	require.Equal(t, defaultWindow, upgradeProtectionWindow)

	// With grace epoch
	require.NoError(t, keeper.AddPunishmentGraceEpoch(ctx, epochIndex, nil, graceWindow))

	upgradeProtectionWindow = defaultWindow
	if graceParams, ok := keeper.GetPunishmentGraceEpoch(ctx, epochIndex); ok && graceParams.UpgradeProtectionWindow > 0 {
		upgradeProtectionWindow = graceParams.UpgradeProtectionWindow
	}
	require.Equal(t, graceWindow, upgradeProtectionWindow)
}

func TestPunishmentGraceEpoch_ZeroUpgradeProtectionWindow(t *testing.T) {
	keeper, ctx, _ := keeper2.InferenceKeeperReturningMocks(t)

	epochIndex := uint64(170)
	defaultWindow := int64(500)

	binomTestP0 := &types.Decimal{Value: 5, Exponent: -1}
	require.NoError(t, keeper.AddPunishmentGraceEpoch(ctx, epochIndex, binomTestP0, 0))

	upgradeProtectionWindow := defaultWindow
	if graceParams, ok := keeper.GetPunishmentGraceEpoch(ctx, epochIndex); ok && graceParams.UpgradeProtectionWindow > 0 {
		upgradeProtectionWindow = graceParams.UpgradeProtectionWindow
	}
	require.Equal(t, defaultWindow, upgradeProtectionWindow)

	graceParams, found := keeper.GetPunishmentGraceEpoch(ctx, epochIndex)
	require.True(t, found)
	require.NotNil(t, graceParams.BinomTestP0)
	require.Equal(t, int64(5), graceParams.BinomTestP0.Value)
}
