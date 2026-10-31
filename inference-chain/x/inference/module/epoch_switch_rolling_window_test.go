package inference

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

// Windows of models that leave the epoch are dropped in the EndBlock that flips the epoch index.
func TestEpochSwitchRemovesDepartedModelWindows(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	k.UpgradeKeeper = noUpgradePlan{}
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	current := types.Epoch{Index: 5, PocStartBlockHeight: 1000}
	require.NoError(t, k.SetEpoch(ctx, &current))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, current.Index))

	height := int64(-1)
	for h := current.PocStartBlockHeight + 1; h < current.PocStartBlockHeight+3*params.EpochParams.EpochLength; h++ {
		ec, err := types.NewEpochContextFromEffectiveEpoch(current, *params.EpochParams, h)
		require.NoError(t, err)
		if ec.IsSetNewValidatorsStage(h) {
			height = h
			break
		}
	}
	require.Positive(t, height)
	next := types.Epoch{Index: 6, PocStartBlockHeight: current.PocStartBlockHeight + params.EpochParams.EpochLength}
	require.NoError(t, k.SetEpoch(ctx, &next))
	k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: 5, PocStartBlockHeight: 1000, SubGroupModels: []string{"m-stay", "m-gone"}})
	k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: 6, PocStartBlockHeight: uint64(next.PocStartBlockHeight), SubGroupModels: []string{"m-stay"}})
	require.NoError(t, k.UpdateModelRollingWindowsForActiveModels(ctx, []string{"m-stay", "m-gone"}, map[string]uint64{"m-gone": 5}, 60))

	_ = NewAppModule(nil, k, nil, nil, nil, nil).EndBlock(ctx.WithBlockHeight(height))

	idx, found := k.GetEffectiveEpochIndex(ctx)
	require.True(t, found)
	require.Equal(t, uint64(6), idx, "epoch index flipped")
	_, found, err = k.GetModelLoadRollingAveragePerBlock(ctx, "m-gone", 12)
	require.NoError(t, err)
	require.False(t, found, "departed model window removed")
	_, found, err = k.GetModelLoadRollingAveragePerBlock(ctx, "m-stay", 12)
	require.NoError(t, err)
	require.True(t, found, "active model window kept")
}
