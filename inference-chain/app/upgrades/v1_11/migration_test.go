package v1_11

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestEpochMigration(t *testing.T) {
	k, sdkCtx := keepertest.InferenceKeeper(t)

	// 1. Prepare initial state: epoch group data (root groups), inferences and validation details
	rootHeights := []uint64{100, 200, 300}
	for _, h := range rootHeights {
		data := types.EpochGroupData{
			PocStartBlockHeight: h,
		}
		k.SetEpochGroupData(sdkCtx, data)
	}

	// Create inferences pointing to the first two epoch groups
	infs := []types.Inference{
		{
			Index:        "inf1",
			InferenceId:  "inf1",
			EpochGroupId: rootHeights[0], // 100
		},
		{
			Index:        "inf2",
			InferenceId:  "inf2",
			EpochGroupId: rootHeights[1], // 200
		},
	}
	for _, inf := range infs {
		k.SetInference(sdkCtx, inf)
	}

	// Create validation detail objects with EpochGroupId not set (legacy behaviour)
	vds := []types.InferenceValidationDetails{
		{
			EpochId:      1,
			InferenceId:  "inf1",
			EpochGroupId: 0,
		},
		{
			EpochId:      2,
			InferenceId:  "inf2",
			EpochGroupId: 0,
		},
	}
	for _, vd := range vds {
		k.SetInferenceValidationDetails(sdkCtx, vd)
	}

	// 2. Run migration helpers directly
	m := createEpochs(sdkCtx, k)
	setEpochIdToInferences(sdkCtx, k, m)
	updateInferenceValidationDetails(sdkCtx, k)

	// 3. Assertions
	// Epochs should be created and effective index set
	for idx, start := range rootHeights {
		expectedEpochId := uint64(idx + 1)

		epoch, found := k.GetEpoch(sdkCtx, expectedEpochId)
		require.True(t, found, "epoch %d should exist", expectedEpochId)
		require.Equal(t, int64(start), epoch.PocStartBlockHeight)

		// EpochGroupData should now carry the epoch id
		eg, found := k.GetEpochGroupData(sdkCtx, start, "")
		require.True(t, found, "epoch group data for start %d should exist", start)
		require.Equal(t, expectedEpochId, eg.EpochId)
	}

	effectiveIdx, found := k.GetEffectiveEpochIndex(sdkCtx)
	require.True(t, found)
	require.Equal(t, uint64(len(rootHeights)), effectiveIdx)

	// Inferences should have updated epoch information
	for _, original := range infs {
		stored, found := k.GetInference(sdkCtx, original.Index)
		require.True(t, found)

		mappedId := m[stored.EpochPocStartBlockHeight]
		require.Equal(t, mappedId, stored.EpochId)
		require.Equal(t, stored.EpochGroupId, stored.EpochPocStartBlockHeight)
	}

	// Validation details should have EpochGroupId == EpochId now
	vafter := k.GetAllInferenceValidationDetails(sdkCtx)
	for _, vd := range vafter {
		require.Equal(t, vd.EpochId, vd.EpochGroupId)
	}
}
