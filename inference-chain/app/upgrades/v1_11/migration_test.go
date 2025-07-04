package v1_11

import (
	"fmt"
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestEpochMigration(t *testing.T) {
	k, sdkCtx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	// ----- 1. Seed data --------------------------------------------------

	const (
		rootEGCount        = 1000
		inferenceCount     = 10000
		validationCount    = 10000
		activeParticipants = 1000
	)

	dummyHeight := func(i int) uint64 {
		return uint64(i+1) * 300
	}

	// 1.a Create epoch groups and sub-groups
	nGroups := uint64(1)
	rootHeights := make([]uint64, rootEGCount)
	for i := 0; i < rootEGCount; i++ {
		h := dummyHeight(i)
		rootHeights[i] = h
		eg, err := k.CreateEpochGroup(sdkCtx, h, uint64(0))
		require.NoError(t, err)
		mocks.ExpectCreateGroupWithPolicyCall(sdkCtx, nGroups)
		nGroups++
		err = eg.CreateGroup(sdkCtx)
		require.NoError(t, err, "test-set-up: Failed to create epoch group for height %d", h)

		ap := types.ActiveParticipants{
			EpochGroupId:        eg.GroupData.EpochGroupId,
			PocStartBlockHeight: int64(h),
			Participants:        nil,
		}
		k.SetActiveParticipantsV1(sdkCtx, ap)

		mocks.ExpectCreateGroupWithPolicyCall(sdkCtx, nGroups)
		nGroups++
		_, err = eg.CreateSubGroup(sdkCtx, "model1")
		require.NoError(t, err, "test-set-up: Failed to create sub group for height %d", h)
		if i > rootEGCount/2 {
			mocks.ExpectCreateGroupWithPolicyCall(sdkCtx, nGroups)
			nGroups++
			_, err = eg.CreateSubGroup(sdkCtx, "model2")
			require.NoError(t, err, "test-set-up: Failed to create second group for height %d", h)
		}
	}

	// 1.c Inferences referencing root epoch groups
	for i := 0; i < inferenceCount; i++ {
		egIdx := rootHeights[i%rootEGCount]
		inf := types.Inference{
			Index:        fmt.Sprintf("inf-%d", i),
			InferenceId:  fmt.Sprintf("inf-%d", i),
			EpochGroupId: egIdx,
		}
		k.SetInferenceWithoutDevStatComputation(sdkCtx, inf)
	}

	// 1.d Validation details (EpochGroupId not filled yet)
	for i := 0; i < validationCount; i++ {
		vd := types.InferenceValidationDetails{
			EpochGroupId: uint64(i%rootEGCount + 1),
			InferenceId:  fmt.Sprintf("inf-%d", i),
		}
		k.SetInferenceValidationDetails(sdkCtx, vd)
	}

	// ----- 2. Run migration helpers --------------------------------------
	mapping := createEpochs(sdkCtx, k)
	setEpochIdToInferences(sdkCtx, k, mapping)
	renameInferenceValidationDetailsEpochId(sdkCtx, k)
	renameActiveParticipantsEpochId(sdkCtx, k, mapping)

	// ----- 3. Assertions --------------------------------------------------

	// Epoch objects exist & effective index is last one
	for idx, height := range rootHeights {
		epochID := uint64(idx + 1)
		epoch, ok := k.GetEpoch(sdkCtx, epochID)
		require.True(t, ok)
		require.Equal(t, int64(height), epoch.PocStartBlockHeight)

		eg, ok := k.GetEpochGroupData(sdkCtx, height, "")
		require.True(t, ok)
		require.Equal(t, epochID, eg.EpochId)
	}
	eff, ok := k.GetEffectiveEpochIndex(sdkCtx)
	require.True(t, ok)
	require.Equal(t, uint64(rootEGCount), eff)

	// Spot-check a few inferences & validation details
	checkIdx := []string{"inf-0", fmt.Sprintf("inf-%d", inferenceCount-1)}
	for _, id := range checkIdx {
		inf, ok := k.GetInference(sdkCtx, id)
		require.True(t, ok)
		require.Equal(t, inf.EpochGroupId, inf.EpochPocStartBlockHeight)
		require.Equal(t, mapping[inf.EpochGroupId], inf.EpochId)
	}

	vds := k.GetAllInferenceValidationDetails(sdkCtx)
	for _, v := range vds {
		require.Equal(t, v.EpochId, v.EpochGroupId)
	}

	// Active participants updated
	for _, h := range rootHeights[:10] { // sample first 10
		ap, ok := k.GetActiveParticipantsV1(sdkCtx, h)
		require.True(t, ok)
		require.Equal(t, mapping[h], ap.EpochId)
	}
}
