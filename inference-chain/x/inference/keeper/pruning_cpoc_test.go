package keeper_test

import (
	"context"
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func setPocV2Stage(t *testing.T, ctx context.Context, k keeper.Keeper, stage int64) {
	p, v := mkAddr(1), mkAddr(2)
	require.NoError(t, k.SetPocValidationV2(ctx, types.PoCValidationV2{
		ParticipantAddress: p, ValidatorParticipantAddress: v, ModelId: "m", PocStageStartBlockHeight: stage, ValidatedWeight: 10,
	}))
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress: p, ModelId: "m", PocStageStartBlockHeight: stage, Count: 5,
	}))
	require.NoError(t, k.SetMLNodeWeightDistribution(ctx, types.MLNodeWeightDistribution{
		ParticipantAddress: p, ModelId: "m", PocStageStartBlockHeight: stage,
		Weights: []*types.MLNodeWeight{{NodeId: "n", Weight: 10}},
	}))
}

// pocV2StageSizes returns votes, commits and distributions stored for stage.
func pocV2StageSizes(t *testing.T, ctx context.Context, k keeper.Keeper, stage int64) [3]int {
	v, err := k.GetPoCValidationsV2ByStage(ctx, stage)
	require.NoError(t, err)
	c, err := k.GetAllPoCV2StoreCommitsForStage(ctx, stage)
	require.NoError(t, err)
	d, err := k.GetAllMLNodeWeightDistributionsForStage(ctx, stage)
	require.NoError(t, err)
	return [3]int{len(v), len(c), len(d)}
}

// Confirmation PoC data is keyed by the event's trigger height, between two regular stages.
func TestPoCV2PruningRemovesConfirmationStages(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))
	setPruningConfig(ctx, k, PruningSettings{PocThreshold: 1, PocMaxPrune: 100})
	for i, h := range []int64{100, 200, 300} {
		require.NoError(t, k.Epochs.Set(ctx, uint64(i+1), types.Epoch{Index: uint64(i + 1), PocStartBlockHeight: h}))
	}
	// 50: confirmation stage left over from an already pruned epoch
	stages := []int64{50, 100, 150, 170, 200, 250, 300}
	for _, s := range stages {
		setPocV2Stage(t, ctx, k, s)
	}

	require.NoError(t, k.Prune(ctx, 2)) // prunes epoch 1
	require.NoError(t, k.Prune(ctx, 2)) // empty pass marks it done

	for _, s := range stages {
		want := [3]int{1, 1, 1}
		if s < 200 {
			want = [3]int{}
		}
		require.Equal(t, want, pocV2StageSizes(t, ctx, k, s), "stage %d", s)
	}
	st, err := k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), st.PocValidationsV2PrunedEpoch)
	require.Equal(t, int64(1), st.PocV2StoreCommitsPrunedEpoch)
	require.Equal(t, int64(1), st.MlnodeWeightDistributionsPrunedEpoch)
}

// Without the next epoch the pruner keeps the old bound: the epoch's own stage only.
func TestPoCV2PruningWithoutNextEpochPrunesOwnStage(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))
	setPruningConfig(ctx, k, PruningSettings{PocThreshold: 1, PocMaxPrune: 100})
	require.NoError(t, k.Epochs.Set(ctx, 1, types.Epoch{Index: 1, PocStartBlockHeight: 100}))
	for _, s := range []int64{100, 150} {
		setPocV2Stage(t, ctx, k, s)
	}

	require.NoError(t, k.Prune(ctx, 2))

	require.Equal(t, [3]int{}, pocV2StageSizes(t, ctx, k, 100))
	require.Equal(t, [3]int{1, 1, 1}, pocV2StageSizes(t, ctx, k, 150))
}

// The cap per block still holds while a backlog of old stages is removed.
func TestPoCV2PruningConfirmationBacklogRespectsMax(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))
	setPruningConfig(ctx, k, PruningSettings{PocThreshold: 1, PocMaxPrune: 2})
	require.NoError(t, k.Epochs.Set(ctx, 1, types.Epoch{Index: 1, PocStartBlockHeight: 100}))
	require.NoError(t, k.Epochs.Set(ctx, 2, types.Epoch{Index: 2, PocStartBlockHeight: 200}))
	for _, s := range []int64{10, 20, 30, 100, 150} {
		setPocV2Stage(t, ctx, k, s)
	}

	left := func() int {
		n := 0
		for _, s := range []int64{10, 20, 30, 100, 150} {
			n += pocV2StageSizes(t, ctx, k, s)[0]
		}
		return n
	}
	require.NoError(t, k.Prune(ctx, 2))
	require.Equal(t, 3, left())
	require.NoError(t, k.Prune(ctx, 2))
	require.NoError(t, k.Prune(ctx, 2))
	require.Equal(t, 0, left())
	st, _ := k.PruningState.Get(ctx)
	require.Equal(t, int64(0), st.PocValidationsV2PrunedEpoch, "epoch 1 is done only after an empty pass")
	require.NoError(t, k.Prune(ctx, 2))
	st, _ = k.PruningState.Get(ctx)
	require.Equal(t, int64(1), st.PocValidationsV2PrunedEpoch)
}

func setPocV1Stage(t *testing.T, ctx context.Context, k keeper.Keeper, stage int64) {
	p, v := mkAddr(1), mkAddr(2)
	require.NoError(t, k.SetPocBatch(ctx, types.PoCBatch{ParticipantAddress: p, PocStageStartBlockHeight: stage, BatchId: "b"}))
	require.NoError(t, k.SetPoCValidation(ctx, types.PoCValidation{ParticipantAddress: p, ValidatorParticipantAddress: v, PocStageStartBlockHeight: stage}))
}

func pocV1StageSizes(t *testing.T, ctx context.Context, k keeper.Keeper, stage int64) [2]uint64 {
	b, err := k.GetPoCBatchesCountByStage(ctx, stage)
	require.NoError(t, err)
	v, err := k.GetPocValidationCountByStage(ctx, stage)
	require.NoError(t, err)
	return [2]uint64{b, v}
}

// PoC v1 confirmation batches and validations are keyed by trigger height too.
func TestPoCV1PruningRemovesConfirmationStages(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))
	setPruningConfig(ctx, k, PruningSettings{PocThreshold: 1, PocMaxPrune: 100})
	for i, h := range []int64{100, 200, 300} {
		require.NoError(t, k.Epochs.Set(ctx, uint64(i+1), types.Epoch{Index: uint64(i + 1), PocStartBlockHeight: h}))
	}
	// 50: confirmation stage left over from an already pruned epoch
	stages := []int64{50, 100, 150, 170, 200, 250, 300}
	for _, s := range stages {
		setPocV1Stage(t, ctx, k, s)
	}

	require.NoError(t, k.Prune(ctx, 2))
	require.NoError(t, k.Prune(ctx, 2))

	for _, s := range stages {
		want := [2]uint64{1, 1}
		if s < 200 {
			want = [2]uint64{}
		}
		require.Equal(t, want, pocV1StageSizes(t, ctx, k, s), "stage %d", s)
	}
	st, err := k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), st.PocBatchesPrunedEpoch)
	require.Equal(t, int64(1), st.PocValidationsPrunedEpoch)
}

// Old confirmation stages below an already pruned epoch go at most PocPruningMax per block.
func TestPoCV1PruningConfirmationBacklogRespectsMax(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{PocBatchesPrunedEpoch: 1, PocValidationsPrunedEpoch: 1}))
	setPruningConfig(ctx, k, PruningSettings{PocThreshold: 1, PocMaxPrune: 2})
	for i, h := range []int64{100, 200, 300} {
		require.NoError(t, k.Epochs.Set(ctx, uint64(i+1), types.Epoch{Index: uint64(i + 1), PocStartBlockHeight: h}))
	}
	stages := []int64{110, 120, 130, 250}
	for _, s := range stages {
		setPocV1Stage(t, ctx, k, s)
	}
	left := func() uint64 {
		n := uint64(0)
		for _, s := range stages[:3] {
			n += pocV1StageSizes(t, ctx, k, s)[0]
		}
		return n
	}

	require.NoError(t, k.Prune(ctx, 3)) // prunes epoch 2: everything below 300
	require.Equal(t, uint64(1), left())
	require.NoError(t, k.Prune(ctx, 3))
	require.Equal(t, uint64(0), left())
	require.Equal(t, [2]uint64{}, pocV1StageSizes(t, ctx, k, 250))
}
