package store_test

import (
	"testing"

	"devshard/testenv/mockchain/seed"
	"devshard/testenv/mockchain/store"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestPlanEpochAdvance_TargetsNextPocStart(t *testing.T) {
	st := seed.Defaults()
	require.Equal(t, int64(500), st.GetNextPocStartBlockHeight())

	plan := st.PlanEpochAdvance()
	require.Equal(t, int64(150), plan.FromHeight)
	require.Equal(t, int64(500), plan.ToHeight)
	require.Equal(t, uint64(2), plan.NewEpoch.Index)
	require.Equal(t, int64(500), plan.NewEpoch.PocStartBlockHeight)
	require.Equal(t, int64(900), plan.NewNextPoc)
}

func TestApplyEpochAdvance_UpdatesNextPocStart(t *testing.T) {
	st := seed.Defaults()
	plan := st.PlanEpochAdvance()
	for h := plan.FromHeight + 1; h <= plan.ToHeight; h++ {
		st.SetBlockHeight(h)
	}
	st.ApplyEpochAdvance(plan)
	require.Equal(t, uint64(2), st.GetEpoch().Index)
	require.Equal(t, int64(500), st.GetBlockHeight())
	require.Equal(t, int64(500), st.GetEpoch().PocStartBlockHeight)
	require.Equal(t, int64(900), st.GetNextPocStartBlockHeight())
}

// Test flow:
//  1. Seed the default store, whose first epoch carries a snapshot of test-model.
//  2. Publish heights up to the planned advance and apply it.
//  3. Assert the epoch-group query for epoch 2 returns test-model's snapshot with the seeded validation threshold.
func TestApplyEpochAdvance_CarriesModelSnapshotsForward(t *testing.T) {
	st := seed.Defaults()
	seeded := st.GetEpochGroupData(1, "test-model")
	require.NotNil(t, seeded)
	plan := st.PlanEpochAdvance()
	for h := plan.FromHeight + 1; h <= plan.ToHeight; h++ {
		st.SetBlockHeight(h)
	}

	st.ApplyEpochAdvance(plan)

	carried := st.GetEpochGroupData(2, "test-model")
	require.NotNil(t, carried)
	require.Equal(t, uint64(2), carried.EpochIndex)
	require.NotNil(t, carried.ModelSnapshot)
	require.Equal(t, "test-model", carried.ModelSnapshot.Id)
	require.Equal(t, seeded.ModelSnapshot.ValidationThreshold, carried.ModelSnapshot.ValidationThreshold)
}

// Test flow:
//  1. Seed the default store and advance it twice without catch-up.
//  2. Assert the epoch-group query for epoch 3 returns test-model's snapshot.
func TestAdvanceEpochWithoutCatchUp_CarriesModelSnapshotsForward(t *testing.T) {
	st := seed.Defaults()

	st.AdvanceEpochWithoutCatchUp()
	st.AdvanceEpochWithoutCatchUp()

	carried := st.GetEpochGroupData(3, "test-model")
	require.NotNil(t, carried)
	require.Equal(t, uint64(3), carried.EpochIndex)
	require.NotNil(t, carried.ModelSnapshot)
	require.Equal(t, "test-model", carried.ModelSnapshot.Id)
}

// Test flow:
//  1. Seed the default store and give epoch 2 its own test-model snapshot with a different validation threshold.
//  2. Apply the planned advance into epoch 2.
//  3. Assert the epoch-2 snapshot is the one set for it, not the carried one.
func TestApplyEpochAdvance_KeepsSnapshotAlreadySetForTheNewEpoch(t *testing.T) {
	st := seed.Defaults()
	own := &inferencetypes.EpochGroupData{
		EpochIndex:    2,
		ModelId:       "test-model",
		ModelSnapshot: &inferencetypes.Model{Id: "test-model", ValidationThreshold: &inferencetypes.Decimal{Value: 7, Exponent: -1}},
	}
	st.EpochGroupData[store.EpochGroupKey{EpochIndex: 2, ModelID: "test-model"}] = own

	st.ApplyEpochAdvance(st.PlanEpochAdvance())

	carried := st.GetEpochGroupData(2, "test-model")
	require.NotNil(t, carried)
	require.Equal(t, own.ModelSnapshot.ValidationThreshold, carried.ModelSnapshot.ValidationThreshold)
}
