package keeper_test

import (
	"context"
	"strconv"
	"testing"

	"cosmossdk.io/collections"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Prevent strconv unused error
var _ = strconv.IntSize

func createNEpochGroupValidations(keeper keeper.Keeper, ctx context.Context, n int) []types.EpochGroupValidations {
	items := make([]types.EpochGroupValidations, n)
	for i := range items {
		items[i].Participant = strconv.Itoa(i)
		items[i].EpochIndex = uint64(i)
		items[i].ValidatedInferences = []string{strconv.Itoa(i)}
		_ = keeper.SeedEpochGroupValidationEntries(ctx, items[i])
	}
	return items
}

func TestEpochGroupValidationsGet(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNEpochGroupValidations(keeper, ctx, 10)
	for _, item := range items {
		rst, found := keeper.GetEpochGroupValidations(ctx,
			item.Participant,
			item.EpochIndex,
		)
		require.True(t, found)
		require.Equal(t,
			nullify.Fill(&item),
			nullify.Fill(&rst),
		)
	}
}

func TestMigrateEpochGroupValidationsToEntries(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	// current epoch 2, previous epoch 1, old epoch 0
	currentEpoch := uint64(2)
	previousEpoch := uint64(1)
	oldEpoch := uint64(0)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpoch))

	// Data to be migrated (current epoch)
	val2 := types.EpochGroupValidations{
		Participant:         "part2",
		EpochIndex:          currentEpoch,
		ValidatedInferences: []string{"inf2-1", "inf2-2"},
	}
	// Data to be migrated (previous epoch)
	val1 := types.EpochGroupValidations{
		Participant:         "part1",
		EpochIndex:          previousEpoch,
		ValidatedInferences: []string{"inf1-1"},
	}
	// Data to NOT be migrated (too old)
	val0 := types.EpochGroupValidations{
		Participant:         "part0",
		EpochIndex:          oldEpoch,
		ValidatedInferences: []string{"inf0-1"},
	}

	// Seed legacy map
	require.NoError(t, k.EpochGroupValidationsMap.Set(ctx, collections.Join(currentEpoch, "part2"), val2))
	require.NoError(t, k.EpochGroupValidationsMap.Set(ctx, collections.Join(previousEpoch, "part1"), val1))
	require.NoError(t, k.EpochGroupValidationsMap.Set(ctx, collections.Join(oldEpoch, "part0"), val0))

	// Pre-condition: check legacy map is populated
	iter, _ := k.EpochGroupValidationsMap.Iterate(ctx, nil)
	vals, _ := iter.Values()
	require.Equal(t, 3, len(vals))

	// Run migration
	err := k.MigrateEpochGroupValidationsToEntries(ctx)
	require.NoError(t, err)

	// Verify current epoch migrated
	mig2, found := k.GetEpochGroupValidations(ctx, "part2", currentEpoch)
	require.True(t, found)
	require.ElementsMatch(t, val2.ValidatedInferences, mig2.ValidatedInferences)

	// Verify previous epoch migrated
	mig1, found := k.GetEpochGroupValidations(ctx, "part1", previousEpoch)
	require.True(t, found)
	require.ElementsMatch(t, val1.ValidatedInferences, mig1.ValidatedInferences)

	// Verify old epoch NOT migrated
	_, found = k.GetEpochGroupValidations(ctx, "part0", oldEpoch)
	require.False(t, found)

	// Verify legacy map is cleared
	iter2, _ := k.EpochGroupValidationsMap.Iterate(ctx, nil)
	vals2, _ := iter2.Values()
	require.Empty(t, vals2)
}

func TestMigrateEpochGroupValidationsToEntries_Idempotency(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	currentEpoch := uint64(1)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpoch))

	val := types.EpochGroupValidations{
		Participant:         "part1",
		EpochIndex:          currentEpoch,
		ValidatedInferences: []string{"inf1"},
	}

	// Seed legacy map
	require.NoError(t, k.EpochGroupValidationsMap.Set(ctx, collections.Join(currentEpoch, "part1"), val))

	// Seed entry map with the same data partially
	require.NoError(t, k.SetEpochGroupValidation(ctx, currentEpoch, "part1", "inf1"))

	// Run migration
	err := k.MigrateEpochGroupValidationsToEntries(ctx)
	require.NoError(t, err)

	// Verify data is still there and correct
	mig, found := k.GetEpochGroupValidations(ctx, "part1", currentEpoch)
	require.True(t, found)
	require.Equal(t, []string{"inf1"}, mig.ValidatedInferences)
}
