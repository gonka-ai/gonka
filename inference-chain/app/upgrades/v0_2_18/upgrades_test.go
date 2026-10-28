package v0_2_18

import (
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// TestUpgradeName pins the future on-chain proposal name. The governance
// proposal and UpgradeName must stay identical or the handler will not run.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.18", UpgradeName)
}

func withLegacyDowntimeThreshold(t *testing.T, bz []byte) []byte {
	threshold, err := types.DecimalFromFloat(0.05).Marshal()
	require.NoError(t, err)
	var legacyCollateral []byte
	legacyCollateral = protowire.AppendTag(legacyCollateral, 3, protowire.BytesType)
	legacyCollateral = protowire.AppendBytes(legacyCollateral, threshold)
	legacy := protowire.AppendTag(append([]byte{}, bz...), 5, protowire.BytesType)
	return protowire.AppendBytes(legacy, legacyCollateral)
}

func inferenceStore(t *testing.T, ctx sdk.Context) storetypes.KVStore {
	ms, ok := ctx.MultiStore().(interface {
		StoreKeysByName() map[string]storetypes.StoreKey
	})
	require.True(t, ok)
	key, ok := ms.StoreKeysByName()[types.StoreKey]
	require.True(t, ok)
	return ctx.KVStore(key)
}

func TestRemovedDowntimeMissedPercentageThresholdIsDropped(t *testing.T) {
	params := types.DefaultParams()
	bz, err := params.Marshal()
	require.NoError(t, err)
	legacy := withLegacyDowntimeThreshold(t, bz)

	var decoded types.Params
	require.NoError(t, decoded.Unmarshal(legacy))
	reencoded, err := decoded.Marshal()
	require.NoError(t, err)
	require.Equal(t, bz, reencoded)
}

func TestDropRemovedParamsPreservesParams(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params := types.DefaultParams()
	params.CollateralParams.SlashFractionDowntime = types.DecimalFromFloat(0.1)
	params.CollateralParams.GracePeriodEndEpoch = 180
	params.CollateralParams.CollateralPerWeightUnit = types.DecimalFromFloat(4.2)
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, dropRemovedParams(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, params, got)
}

func TestDropRemovedParamsClearsLegacyStoredBytes(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params := types.DefaultParams()
	require.NoError(t, k.SetParams(ctx, params))

	store := inferenceStore(t, ctx)
	clean := store.Get(types.ParamsKey)
	require.NotNil(t, clean)
	legacy := withLegacyDowntimeThreshold(t, clean)
	store.Set(types.ParamsKey, legacy)
	require.NotEqual(t, clean, store.Get(types.ParamsKey))

	require.NoError(t, dropRemovedParams(ctx, k))

	require.Equal(t, clean, store.Get(types.ParamsKey))
	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, params, got)
}
