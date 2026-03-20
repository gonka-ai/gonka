package keeper_test

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestEpochDataTransientCache_BuildAndGet(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	// Setup data for Epoch 1
	epoch1 := uint64(1)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epoch1))

	rootGroup1 := types.EpochGroupData{
		EpochIndex:     epoch1,
		ModelId:        "",
		EpochPolicy:    "policy1",
		TotalWeight:    100,
		SubGroupModels: []string{"modelA", "modelB"},
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: "val1", Weight: 50, Reputation: 10},
			{MemberAddress: "val2", Weight: 50, Reputation: 20},
		},
	}
	k.SetEpochGroupData(ctx, rootGroup1)

	modelAGroup1 := types.EpochGroupData{
		EpochIndex:  epoch1,
		ModelId:     "modelA",
		EpochPolicy: "policyA",
		TotalWeight: 30,
		ModelSnapshot: &types.Model{
			ValidationThreshold: &types.Decimal{Value: 5, Exponent: 1},
		},
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: "val1", Weight: 30, Reputation: 10},
		},
	}
	k.SetEpochGroupData(ctx, modelAGroup1)

	// Build cache
	err := k.BuildEpochDataTransientCache(ctx)
	require.NoError(t, err)

	// Verify Root Meta
	meta, found, err := k.GetCachedEpochDataModelMeta(ctx, epoch1, "")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "policy1", meta.EpochPolicy)
	require.Equal(t, int64(100), meta.TotalWeight)
	require.Equal(t, []string{"modelA", "modelB"}, meta.SubGroupModels)

	// Verify ModelA Meta
	metaA, found, err := k.GetCachedEpochDataModelMeta(ctx, epoch1, "modelA")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "policyA", metaA.EpochPolicy)
	require.Equal(t, int64(30), metaA.TotalWeight)
	require.NotNil(t, metaA.ValidationThreshold)
	require.Equal(t, int64(5), metaA.ValidationThreshold.Value)
	require.Equal(t, int32(1), metaA.ValidationThreshold.Exponent)

	// Verify Weights
	w1, found, err := k.GetCachedEpochDataModelWeight(ctx, epoch1, "", "val1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(50), w1.Weight)
	require.Equal(t, int32(10), w1.Reputation)

	wA1, found, err := k.GetCachedEpochDataModelWeight(ctx, epoch1, "modelA", "val1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(30), wA1.Weight)
	require.Equal(t, int32(10), wA1.Reputation)

	// Verify ModelB (not in store, should not be in cache)
	_, found, err = k.GetCachedEpochDataModelMeta(ctx, epoch1, "modelB")
	require.NoError(t, err)
	require.False(t, found)
}

func TestEpochDataTransientCache_ModelNameWithSlash(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	epoch := uint64(1)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epoch))

	modelWithSlash := "org/model/v1"
	groupData := types.EpochGroupData{
		EpochIndex:  epoch,
		ModelId:     modelWithSlash,
		EpochPolicy: "policy-slash",
		TotalWeight: 50,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: "val1", Weight: 50, Reputation: 10},
		},
	}

	// Root group must exist and contain the model in SubGroupModels
	rootGroup := types.EpochGroupData{
		EpochIndex:     epoch,
		ModelId:        "",
		SubGroupModels: []string{modelWithSlash},
	}

	k.SetEpochGroupData(ctx, rootGroup)
	k.SetEpochGroupData(ctx, groupData)

	require.NoError(t, k.BuildEpochDataTransientCache(ctx))

	// Verify Meta
	meta, found, err := k.GetCachedEpochDataModelMeta(ctx, epoch, modelWithSlash)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "policy-slash", meta.EpochPolicy)
	require.Equal(t, int64(50), meta.TotalWeight)

	// Verify Weight
	w, found, err := k.GetCachedEpochDataModelWeight(ctx, epoch, modelWithSlash, "val1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(50), w.Weight)
}

func TestEpochDataTransientCache_MultiEpoch(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	// Setup Epoch 1 and Epoch 2
	epoch1 := uint64(1)
	epoch2 := uint64(2)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epoch2))

	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:  epoch1,
		ModelId:     "",
		TotalWeight: 100,
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:  epoch2,
		ModelId:     "",
		TotalWeight: 200,
	})

	require.NoError(t, k.BuildEpochDataTransientCache(ctx))

	// Both should be cached as per cachedEpochDataEpochs logic (current and previous)
	meta1, found, _ := k.GetCachedEpochDataModelMeta(ctx, epoch1, "")
	require.True(t, found)
	require.Equal(t, int64(100), meta1.TotalWeight)

	meta2, found, _ := k.GetCachedEpochDataModelMeta(ctx, epoch2, "")
	require.True(t, found)
	require.Equal(t, int64(200), meta2.TotalWeight)

	// Epoch 0 should not be cached if we are at Epoch 2
	_, found, _ = k.GetCachedEpochDataModelMeta(ctx, 0, "")
	require.False(t, found)
}

func TestEpochDataTransientCache_Empty(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	// No effective epoch index set
	err := k.BuildEpochDataTransientCache(ctx)
	require.NoError(t, err)

	_, found, err := k.GetCachedEpochDataModelMeta(ctx, 1, "")
	require.NoError(t, err)
	require.False(t, found)
}

func TestEpochDataTransientCache_Epoch0(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 0))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:  0,
		ModelId:     "",
		TotalWeight: 50,
	})

	require.NoError(t, k.BuildEpochDataTransientCache(ctx))

	meta, found, _ := k.GetCachedEpochDataModelMeta(ctx, 0, "")
	require.True(t, found)
	require.Equal(t, int64(50), meta.TotalWeight)
}
