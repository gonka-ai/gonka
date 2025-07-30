package v1_11

import (
	"context"
	"fmt"
	"testing"

	"github.com/productscience/inference/x/inference/keeper"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestEpochMigration(t *testing.T) {
	t.Skip("Skipping epoch migration test, as APIs has changed since migration.")

	k, sdkCtx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	const (
		rootEGCount     = 1000
		inferenceCount  = 10000
		validationCount = 10000
	)

	dummyHeight := func(i int) uint64 {
		return uint64(i) * 300
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
		// Commented, because API changed since migration
		//_, err = eg.CreateSubGroup(sdkCtx, "model1")
		require.NoError(t, err, "test-set-up: Failed to create sub group for height %d", h)
		if i > rootEGCount/2 {
			mocks.ExpectCreateGroupWithPolicyCall(sdkCtx, nGroups)
			nGroups++
			// Commented, because API changed since migration
			//_, err = eg.CreateSubGroup(sdkCtx, "model2")
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
			// EpochGroupId: uint64(i%rootEGCount + 1),
			InferenceId: fmt.Sprintf("inf-%d", i),
		}
		k.SetInferenceValidationDetails(sdkCtx, vd)
	}

	// ----- 2. Run migration helpers --------------------------------------
	mapping := createEpochs(sdkCtx, k)
	propagateEpochIdToSubGroups(sdkCtx, k, mapping)
	setEpochIdToInferences(sdkCtx, k, mapping)
	renameInferenceValidationDetailsEpochId(sdkCtx, k)
	renameActiveParticipantsEpochId(sdkCtx, k, mapping)
	validateRootEpochSpacing(sdkCtx, k)

	// ----- 3. Assertions --------------------------------------------------

	// Epoch objects exist & effective index is last one
	for i, height := range rootHeights {
		epochId := uint64(i)
		epoch, ok := k.GetEpoch(sdkCtx, epochId)
		require.True(t, ok)
		require.Equal(t, int64(height), epoch.PocStartBlockHeight)

		eg, ok := k.GetEpochGroupData(sdkCtx, height, "")
		require.True(t, ok)
		require.Equal(t, epochId, eg.EpochId)

		ap, ok := k.GetActiveParticipants(sdkCtx, epochId)
		require.True(t, ok)
		require.Equal(t, mapping[height], ap.EpochId)
		require.Equal(t, height, eg.PocStartBlockHeight)
	}
	eff, ok := k.GetEffectiveEpochIndex(sdkCtx)
	require.True(t, ok)
	require.Equal(t, uint64(rootEGCount)-1, eff)

	// Spot-check a few inferences & validation details
	assertInference(t, sdkCtx, k, mapping)

	vds := k.GetAllInferenceValidationDetails(sdkCtx)
	require.Equal(t, validationCount, len(vds))
	// EpochGroupId was deleted
	/*	for _, v := range vds {
		require.Equal(t, v.EpochId, v.EpochGroupId)
	}*/

	// Verify that all sub-groups received the correct EpochId
	allEGs := k.GetAllEpochGroupData(sdkCtx)
	for _, egd := range allEGs {
		if egd.ModelId == "" {
			continue // root handled above
		}
		expectedId, ok := mapping[egd.PocStartBlockHeight]
		require.True(t, ok)
		require.Equal(t, expectedId, egd.EpochId, "Sub-group at height %d, model %s has incorrect EpochId", egd.PocStartBlockHeight, egd.ModelId)
	}
}

func assertInference(t *testing.T, ctx context.Context, k keeper.Keeper, mapping map[uint64]uint64) {
	store := keeper.PrefixStore(ctx, &k, []byte(types.InferenceKeyPrefix))
	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var inf types.Inference
		k.Codec().MustUnmarshal(iterator.Value(), &inf)

		require.Equal(t, inf.EpochGroupId, inf.EpochPocStartBlockHeight)

		epochId, ok := mapping[inf.EpochGroupId]
		require.True(t, ok)

		require.Equal(t, epochId, inf.EpochId, "Inference %s has incorrect EpochId", inf.InferenceId)
	}
}
