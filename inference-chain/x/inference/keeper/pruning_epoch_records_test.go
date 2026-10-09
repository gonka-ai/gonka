package keeper_test

import (
	"encoding/binary"
	"fmt"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func seqAddr(i int) sdk.AccAddress {
	b := make([]byte, 20)
	binary.BigEndian.PutUint32(b[16:], uint32(i))
	return sdk.AccAddress(b)
}

func countPrefixed[V any](t *testing.T, ctx sdk.Context, m collections.Map[collections.Pair[uint64, sdk.AccAddress], V], epoch uint64) int {
	it, err := m.Iterate(ctx, collections.NewPrefixedPairRange[uint64, sdk.AccAddress](epoch))
	require.NoError(t, err)
	defer it.Close()
	n := 0
	for ; it.Valid(); it.Next() {
		n++
	}
	return n
}

func TestPruneEpochRecordsBeyondThreshold(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	const current = 10
	for e := uint64(1); e <= current; e++ {
		for i := 0; i < 3; i++ {
			addr := seqAddr(i)
			require.NoError(t, k.SetRandomSeed(ctx, types.RandomSeed{Participant: addr.String(), EpochIndex: e, Signature: "abcd"}))
			require.NoError(t, k.SetConfirmationPoCEvent(ctx, types.ConfirmationPoCEvent{EpochIndex: e, EventSequence: uint64(i)}))
		}
	}

	require.NoError(t, k.Prune(ctx, current))

	last := int64(current) - int64(keeper.EpochRecordPruningThreshold)
	for e := uint64(1); e <= current; e++ {
		want := 3
		if int64(e) <= last {
			want = 0
		}
		require.Equal(t, want, countPrefixed(t, ctx, k.RandomSeeds, e), "seeds epoch %d", e)
		events, err := k.GetAllConfirmationPoCEventsForEpoch(ctx, e)
		require.NoError(t, err)
		require.Len(t, events, want, "cpoc events epoch %d", e)
	}
	// Seeds the chain still reads (current and upcoming epoch) survive.
	_, found := k.GetRandomSeed(ctx, current, seqAddr(0).String())
	require.True(t, found)

	// An epoch is marked done on the call that finds it empty.
	require.NoError(t, k.Prune(ctx, current))
	state, err := k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, last, state.RandomSeedsPrunedEpoch)
	require.Equal(t, last, state.ConfirmationPocEventsPrunedEpoch)
}

// A backlog of many epochs (mainnet: ~76k seeds over 412 epochs) is removed at most
// PruningMax per block instead of in one block.
func TestPruneEpochRecordsBacklogBoundedPerBlock(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	const perEpoch = 600
	for e := uint64(1); e <= 3; e++ {
		for i := 0; i < perEpoch; i++ {
			require.NoError(t, k.SetRandomSeed(ctx, types.RandomSeed{Participant: seqAddr(i).String(), EpochIndex: e, Signature: "abcd"}))
		}
	}
	remaining := func() int {
		n := 0
		for e := uint64(1); e <= 3; e++ {
			n += countPrefixed(t, ctx, k.RandomSeeds, e)
		}
		return n
	}
	current := int64(3 + keeper.EpochRecordPruningThreshold)
	pruner := k.GetRandomSeedPruner()

	require.NoError(t, pruner.Prune(ctx, k, current))
	require.Equal(t, 3*perEpoch-int(keeper.EpochRecordPruningMaxPerBlock), remaining())

	require.NoError(t, pruner.Prune(ctx, k, current))
	require.Equal(t, 0, remaining())

	require.NoError(t, pruner.Prune(ctx, k, current))
	state, err := k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(3), state.RandomSeedsPrunedEpoch)
}

// Seed and CPoC event pruners run in the Prune rotation and draw on the shared
// PruneWorkPerBlock budget: a block where the inference backlog goes first and takes
// the whole budget removes no seeds; a block where the seed pruner goes first removes
// its PruningMax and leaves the rest of the budget to the others.
func TestPruneEpochRecordsShareBudget(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))
	setPruningConfig(ctx, k, PruningSettings{InferenceThreshold: 2, InferenceMaxPrune: keeper.PruneWorkPerBlock})

	const current = 20
	for e := uint64(1); e <= 5; e++ {
		for i := 0; i < 2000; i++ {
			k.SetInference(ctx, types.Inference{Index: fmt.Sprintf("inf-%d-%d", e, i), EpochId: e, Status: types.InferenceStatus_FINISHED})
		}
	}
	const seeds = 3000
	for i := 0; i < seeds; i++ {
		require.NoError(t, k.SetRandomSeed(ctx, types.RandomSeed{Participant: seqAddr(i).String(), EpochIndex: 1, Signature: "abcd"}))
	}

	// Rotation order in Prune: the inference pruner is at index 0, the seed pruner at 11.
	const pruners = 13
	inf := countInferencesToPrune(t, k, ctx, 5)
	require.NoError(t, k.Prune(ctx.WithBlockHeight(pruners*7), current))
	inf2 := countInferencesToPrune(t, k, ctx, 5)
	require.Equal(t, int(keeper.PruneWorkPerBlock), inf-inf2)
	require.Equal(t, seeds, countPrefixed(t, ctx, k.RandomSeeds, 1))

	require.NoError(t, k.Prune(ctx.WithBlockHeight(pruners*7+11), current))
	inf3 := countInferencesToPrune(t, k, ctx, 5)
	require.Equal(t, seeds-int(keeper.EpochRecordPruningMaxPerBlock), countPrefixed(t, ctx, k.RandomSeeds, 1))
	require.Equal(t, int(keeper.PruneWorkPerBlock-keeper.EpochRecordPruningMaxPerBlock), inf2-inf3)
}
