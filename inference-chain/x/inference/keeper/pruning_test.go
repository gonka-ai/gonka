package keeper_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

type PruningSettings struct {
	InferenceThreshold int64
	PocThreshold       int64
	InferenceMaxPrune  int64
	PocMaxPrune        int64
}

func setPruningConfig(ctx context.Context, k keeper.Keeper, settings PruningSettings) {
	params, err := k.GetParams(ctx)
	if err != nil {
		panic(err)
	}
	if settings.InferenceThreshold > 0 {
		params.EpochParams.InferencePruningEpochThreshold = uint64(settings.InferenceThreshold)
	}
	if settings.PocThreshold > 0 {
		params.PocParams.PocDataPruningEpochThreshold = uint64(settings.PocThreshold)
	}
	if settings.InferenceMaxPrune > 0 {
		params.EpochParams.InferencePruningMax = settings.InferenceMaxPrune
	}
	if settings.PocMaxPrune > 0 {
		params.EpochParams.PocPruningMax = settings.PocMaxPrune
	}
	_ = k.SetParams(ctx, params)
}

func mkAddr(i int) string {
	b := bytes.Repeat([]byte{byte(i)}, 20)
	return sdk.AccAddress(b).String()
}

// TestPruningBasic tests the basic functionality of the pruning system
func TestPruningBasic(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	err := k.PruningState.Set(ctx, types.PruningState{})

	require.NoError(t, err)
	// Create a test inference
	inference := types.Inference{
		Index:   "test-inference",
		EpochId: 1,
		Status:  types.InferenceStatus_FINISHED,
	}

	// Add inference to the store without calculating developer stats
	k.SetInferenceWithoutDevStatComputation(ctx, inference)

	// Verify inference exists
	_, found := k.GetInference(ctx, "test-inference")
	require.True(t, found, "Inference should exist before pruning")

	// Run pruning with a threshold that should prune the inference
	err = k.Prune(ctx, 4) // Current epoch 4, threshold 2
	require.NoError(t, err)

	// Verify inference was pruned
	_, found = k.GetInference(ctx, "test-inference")
	require.False(t, found, "Inference should be pruned")
	pruningState, err := k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), pruningState.InferencePrunedEpoch, "Pruning epoch should be 2")
	require.Equal(t, int64(3), pruningState.PocValidationsPrunedEpoch, "Pruning epoch should be 3")
	require.Equal(t, int64(3), pruningState.PocBatchesPrunedEpoch, "Pruning epoch should be 3")
	err = k.Prune(ctx, 4)
	require.NoError(t, err)
	require.Equal(t, int64(2), pruningState.InferencePrunedEpoch, "Pruning epoch should be 2")
	require.Equal(t, int64(3), pruningState.PocValidationsPrunedEpoch, "Pruning epoch should be 3")
	require.Equal(t, int64(3), pruningState.PocBatchesPrunedEpoch, "Pruning epoch should be 3")
}

// TestPruningEpochThreshold tests that only inferences older than the threshold are pruned
func TestPruningEpochThreshold(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	err := k.PruningState.Set(ctx, types.PruningState{})

	// Create inferences with different epoch IDs
	inferences := []types.Inference{
		{
			Index:   "inference-epoch1",
			EpochId: 1, // Old enough to be pruned
			Status:  types.InferenceStatus_FINISHED,
		},
		{
			Index:   "inference-epoch2",
			EpochId: 2, // Old enough to be pruned
			Status:  types.InferenceStatus_FINISHED,
		},
		{
			Index:   "inference-epoch3",
			EpochId: 3, // Not old enough to be pruned
			Status:  types.InferenceStatus_FINISHED,
		},
		{
			Index:   "inference-epoch4",
			EpochId: 4, // Current epoch, should not be pruned
			Status:  types.InferenceStatus_FINISHED,
		},
	}

	// Add inferences to the store without calculating developer stats
	for _, inf := range inferences {
		k.SetInferenceWithoutDevStatComputation(ctx, inf)
	}

	// Run pruning with threshold 2
	err = k.Prune(ctx, 4) // Current epoch 4, threshold 2
	require.NoError(t, err)

	// Verify correct inferences were pruned
	_, found := k.GetInference(ctx, "inference-epoch1")
	require.False(t, found, "Inference from epoch 1 should be pruned")

	_, found = k.GetInference(ctx, "inference-epoch2")
	require.False(t, found, "Inference from epoch 2 should be pruned")

	_, found = k.GetInference(ctx, "inference-epoch3")
	require.True(t, found, "Inference from epoch 3 should not be pruned")

	_, found = k.GetInference(ctx, "inference-epoch4")
	require.True(t, found, "Inference from epoch 4 should not be pruned")
}

// TestPruningStatusPreservation tests that inferences with VOTING and STARTED status are not pruned
func TestPruningStatusPreservation(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	err := k.PruningState.Set(ctx, types.PruningState{})

	// Create inferences with different statuses
	inferences := []types.Inference{
		{
			Index:   "inference-voting",
			EpochId: 1,
			Status:  types.InferenceStatus_VOTING,
		},
		{
			Index:   "inference-started",
			EpochId: 1,
			Status:  types.InferenceStatus_STARTED,
		},
		{
			Index:   "inference-finished",
			EpochId: 1,
			Status:  types.InferenceStatus_FINISHED,
		},
	}

	// Add inferences to the store
	for _, inf := range inferences {
		k.SetInferenceWithoutDevStatComputation(ctx, inf)
	}

	// Run pruning with threshold that should prune old inferences
	err = k.Prune(ctx, 4) // Current epoch 4, threshold 2
	require.NoError(t, err)

	// Verify VOTING inference was not pruned
	_, found := k.GetInference(ctx, "inference-voting")
	require.True(t, found, "Inference with VOTING status should not be pruned")

	// Verify STARTED inference was not pruned
	_, found = k.GetInference(ctx, "inference-started")
	require.True(t, found, "Inference with STARTED status should not be pruned")

	// Verify FINISHED inference was pruned
	_, found = k.GetInference(ctx, "inference-finished")
	require.False(t, found, "Inference with FINISHED status should be pruned")
}

func TestPruningMultipleEpochs(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	err := k.PruningState.Set(ctx, types.PruningState{})

	// Create inferences for 10 epochs
	inferences := []types.Inference{}
	for i := 1; i <= 10; i++ {
		inferences = append(inferences, types.Inference{
			Index:   fmt.Sprintf("inference-epoch%d", i),
			EpochId: uint64(i),
			Status:  types.InferenceStatus_FINISHED,
		})
	}

	// Add inferences to the store
	for _, inf := range inferences {
		k.SetInferenceWithoutDevStatComputation(ctx, inf)
	}

	// Run pruning with threshold 1 at epoch 10
	setPruningConfig(ctx, k, PruningSettings{
		InferenceThreshold: 1,
	})
	err = k.Prune(ctx, 10)
	require.NoError(t, err)

	// With threshold 1 and current epoch 10, we prune up to epoch 9
	for i := 1; i <= 9; i++ {
		_, found := k.GetInference(ctx, fmt.Sprintf("inference-epoch%d", i))
		require.False(t, found, fmt.Sprintf("Inference from epoch %d should be pruned", i))
	}

	// Epoch 10 should remain
	_, found := k.GetInference(ctx, "inference-epoch10")
	require.True(t, found, "Inference from epoch 10 should not be pruned")
}

// TestInferencePruningMaxLimit_MultiCall_EpochAdvanceAfterEmpty ensures we respect the per-call max
// and only advance the InferencePrunedEpoch after a subsequent call when the epoch becomes empty
func TestInferencePruningMaxLimit_MultiCall_EpochAdvanceAfterEmpty(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	// Create 10 finished inferences all in epoch 1
	for i := 0; i < 10; i++ {
		inf := types.Inference{
			Index:   fmt.Sprintf("inf-%d", i),
			EpochId: 1,
			Status:  types.InferenceStatus_FINISHED,
		}
		_ = k.SetInferenceWithoutDevStatComputation(ctx, inf)
	}

	// Configure pruning: inference threshold 2 so endEpoch=current-2, and max per call 4
	setPruningConfig(ctx, k, PruningSettings{InferenceThreshold: 2, InferenceMaxPrune: 4})

	// Choose current epoch so that only epoch 1 is eligible (endEpoch = 1)
	current := int64(3)

	countRemaining := func() int {
		c := 0
		for i := 0; i < 10; i++ {
			if _, found := k.GetInference(ctx, fmt.Sprintf("inf-%d", i)); found {
				c++
			}
		}
		return c
	}

	// 1st prune: remove 4
	require.NoError(t, k.Prune(ctx, current))
	require.Equal(t, 6, countRemaining())
	st, _ := k.PruningState.Get(ctx)
	require.Equal(t, int64(0), st.InferencePrunedEpoch, "should not advance pruned epoch until epoch becomes empty and a subsequent call occurs")

	// 2nd prune: remove 4 (total 8 removed)
	require.NoError(t, k.Prune(ctx, current))
	require.Equal(t, 2, countRemaining())
	st, _ = k.PruningState.Get(ctx)
	require.Equal(t, int64(0), st.InferencePrunedEpoch)

	// 3rd prune: remove last 2
	require.NoError(t, k.Prune(ctx, current))
	require.Equal(t, 0, countRemaining())
	st, _ = k.PruningState.Get(ctx)
	require.Equal(t, int64(0), st.InferencePrunedEpoch, "still not advanced in same call when items were pruned")

	// 4th prune: nothing to prune in epoch 1, now marker should advance
	require.NoError(t, k.Prune(ctx, current))
	st, _ = k.PruningState.Get(ctx)
	require.Equal(t, int64(1), st.InferencePrunedEpoch, "should advance after verifying epoch is empty")
}

// TestPoCValidationsPruningMaxLimit_MultiCall_EpochAdvanceAfterEmpty mirrors the inference case
func TestPoCValidationsPruningMaxLimit_MultiCall_EpochAdvanceAfterEmpty(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	prunedEpoch := int64(2)
	prunedEpochBlockHeight := int64(20)
	// Create 10 validations in epoch 2 (eligible when current=3, threshold=1, end=2)
	p := mkAddr(1)
	for i := 0; i < 10; i++ {
		v := mkAddr(100 + i)
		err := k.SetPoCValidation(ctx, types.PoCValidation{
			ParticipantAddress:          p,
			ValidatorParticipantAddress: v,
			PocStageStartBlockHeight:    prunedEpochBlockHeight,
		})
		require.NoError(t, err)
	}

	// Configure pruning: PoC threshold 1, PoC max prune 4
	setPruningConfig(ctx, k, PruningSettings{PocThreshold: 1, PocMaxPrune: 4})
	current := int64(3) // start=1, end=2

	getCount := func() uint64 {
		c, err := k.GetPocValidationCountByStage(ctx, prunedEpochBlockHeight)
		require.NoError(t, err)
		return c
	}
	err := k.Epochs.Set(ctx, uint64(prunedEpoch), types.Epoch{
		Index:               uint64(prunedEpoch),
		PocStartBlockHeight: prunedEpochBlockHeight,
	})
	require.NoError(t, err)

	// 1st prune: epoch 1 empty so marker may become 1, epoch 2 prunes 4
	require.NoError(t, k.Prune(ctx, current))
	require.Equal(t, uint64(6), getCount())
	st, _ := k.PruningState.Get(ctx)
	require.Equal(t, int64(1), st.PocValidationsPrunedEpoch, "should only be advanced past empty epochs; not the non-empty epoch 2")

	// 2nd prune: remove 4 more (left 2)
	require.NoError(t, k.Prune(ctx, current))
	require.Equal(t, uint64(2), getCount())
	st, _ = k.PruningState.Get(ctx)
	require.Equal(t, int64(1), st.PocValidationsPrunedEpoch)

	// 3rd prune: remove last 2
	require.NoError(t, k.Prune(ctx, current))
	require.Equal(t, uint64(0), getCount())
	st, _ = k.PruningState.Get(ctx)
	require.Equal(t, int64(1), st.PocValidationsPrunedEpoch, "not advanced in same call")

	// 4th prune: epoch 2 now empty, marker should advance to 2
	require.NoError(t, k.Prune(ctx, current))
	st, _ = k.PruningState.Get(ctx)
	require.Equal(t, int64(2), st.PocValidationsPrunedEpoch)
}

// TestPoCBatchesPruningMaxLimit_MultiCall_EpochAdvanceAfterEmpty mirrors the validations case
func TestPoCBatchesPruningMaxLimit_MultiCall_EpochAdvanceAfterEmpty(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	prunedEpoch := 2
	prunedEpochBlockHeight := int64(20)
	// Create 10 batches in epoch 2 (eligible when current=3, threshold=1, end=2)
	p := mkAddr(2)
	for i := 0; i < 10; i++ {
		err := k.SetPocBatch(ctx, types.PoCBatch{
			ParticipantAddress:       p,
			PocStageStartBlockHeight: prunedEpochBlockHeight,
			BatchId:                  fmt.Sprintf("b-%d", i),
		})
		require.NoError(t, err)
	}

	// Configure pruning: PoC threshold 1, PoC max prune 4
	setPruningConfig(ctx, k, PruningSettings{PocThreshold: 1, PocMaxPrune: 4})
	current := int64(3)

	getCount := func() uint64 {
		c, err := k.GetPoCBatchesCountByStage(ctx, prunedEpochBlockHeight)
		require.NoError(t, err)
		return c
	}
	err := k.Epochs.Set(ctx, uint64(prunedEpoch), types.Epoch{
		Index:               uint64(prunedEpoch),
		PocStartBlockHeight: prunedEpochBlockHeight,
	})
	require.NoError(t, err)

	// 1st prune
	require.NoError(t, k.Prune(ctx, current))
	require.Equal(t, uint64(6), getCount())
	st, _ := k.PruningState.Get(ctx)
	require.Equal(t, int64(1), st.PocBatchesPrunedEpoch)

	// 2nd prune
	require.NoError(t, k.Prune(ctx, current))
	require.Equal(t, uint64(2), getCount())
	st, _ = k.PruningState.Get(ctx)
	require.Equal(t, int64(1), st.PocBatchesPrunedEpoch)

	// 3rd prune: empty the epoch
	require.NoError(t, k.Prune(ctx, current))
	require.Equal(t, uint64(0), getCount())
	st, _ = k.PruningState.Get(ctx)
	require.Equal(t, int64(1), st.PocBatchesPrunedEpoch)

	// 4th prune: advance epoch marker
	require.NoError(t, k.Prune(ctx, current))
	st, _ = k.PruningState.Get(ctx)
	require.Equal(t, int64(2), st.PocBatchesPrunedEpoch)
}
