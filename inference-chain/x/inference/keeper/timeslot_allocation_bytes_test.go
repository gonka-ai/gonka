package keeper_test

import (
	"fmt"
	"testing"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// nodeGroup is the mainnet epoch 416 MiniMax sub-group: 25 members, 155 ML nodes,
// every timeslot_allocation [true, false].
func nodeGroup() types.EpochGroupData {
	egd := seedSignedGroup(25)
	for i := 0; i < 155; i++ {
		w := egd.ValidationWeights[i%25]
		w.MlNodes = append(w.MlNodes, &types.MLNodeInfo{
			NodeId:             fmt.Sprintf("mlnode-%d", 300+i),
			Throughput:         5000,
			PocWeight:          6400 + int64(i),
			TimeslotAllocation: []bool{true, false},
		})
	}
	return egd
}

func TestEpochGroupData_DefaultTimeslotsLeftOut(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	want := nodeGroup()
	given := cloneGroup(t, want)
	writeCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	k.SetEpochGroupData(writeCtx, given)
	require.Equal(t, want, given, "caller's group data must not change")

	raw, err := k.EpochGroupDataMap.Get(ctx, collections.Join(want.EpochIndex, want.ModelId))
	require.NoError(t, err)
	require.True(t, raw.DefaultTimeslotAllocations)
	for _, w := range raw.ValidationWeights {
		for _, n := range w.MlNodes {
			require.Empty(t, n.TimeslotAllocation)
		}
	}
	newBytes, err := raw.Marshal()
	require.NoError(t, err)

	// The previous storage form: the same record with every allocation written out.
	prev := cloneGroup(t, raw)
	prev.DefaultTimeslotAllocations = false
	for _, w := range prev.ValidationWeights {
		for _, n := range w.MlNodes {
			n.TimeslotAllocation = []bool{true, false}
		}
	}
	prev.EpochIndex = want.EpochIndex + 1
	prevWrite := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.EpochGroupDataMap.Set(prevWrite, collections.Join(prev.EpochIndex, prev.ModelId), prev))
	prevBytes, err := prev.Marshal()
	require.NoError(t, err)

	readPrev := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	gotPrev, found := k.GetEpochGroupData(readPrev, prev.EpochIndex, prev.ModelId)
	require.True(t, found)
	wantPrev := cloneGroup(t, want)
	wantPrev.EpochIndex = prev.EpochIndex
	require.Equal(t, wantPrev, gotPrev)

	readCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	got, found := k.GetEpochGroupData(readCtx, want.EpochIndex, want.ModelId)
	require.True(t, found)
	require.Equal(t, want, got)

	t.Logf("155-node sub-group value %d -> %d B; write %d -> %d gas; read %d -> %d gas",
		len(prevBytes), len(newBytes), prevWrite.GasMeter().GasConsumed(), writeCtx.GasMeter().GasConsumed(),
		readPrev.GasMeter().GasConsumed(), readCtx.GasMeter().GasConsumed())
	require.Less(t, readCtx.GasMeter().GasConsumed(), readPrev.GasMeter().GasConsumed())

	cached := keeper.WithTxParamsCache(ctx)
	got, found = k.GetEpochGroupData(cached, want.EpochIndex, want.ModelId)
	require.True(t, found)
	require.Equal(t, want, got)

	all, err := k.EpochGroupDataAll(ctx, &types.QueryAllEpochGroupDataRequest{})
	require.NoError(t, err)
	require.Equal(t, []types.EpochGroupData{want, wantPrev}, all.EpochGroupData)
	require.Equal(t, []types.EpochGroupData{want, wantPrev}, k.GetAllEpochGroupData(ctx))
}

func TestEpochGroupData_OtherTimeslotsKept(t *testing.T) {
	for name, alloc := range map[string][]bool{
		"poc slot":  {true, true},
		"empty":     nil,
		"one":       {true},
		"pre false": {false, false},
	} {
		t.Run(name, func(t *testing.T) {
			k, ctx := keepertest.InferenceKeeper(t)
			want := nodeGroup()
			want.ValidationWeights[3].MlNodes[1].TimeslotAllocation = alloc
			k.SetEpochGroupData(ctx, cloneGroup(t, want))

			raw, err := k.EpochGroupDataMap.Get(ctx, collections.Join(want.EpochIndex, want.ModelId))
			require.NoError(t, err)
			require.False(t, raw.DefaultTimeslotAllocations)
			require.Equal(t, []bool{true, false}, raw.ValidationWeights[0].MlNodes[0].TimeslotAllocation)

			got, found := k.GetEpochGroupData(ctx, want.EpochIndex, want.ModelId)
			require.True(t, found)
			require.Equal(t, want, got)
		})
	}
}

func TestEpochGroupData_NoNodesNoFlag(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	want := seedSignedGroup(3)
	k.SetEpochGroupData(ctx, cloneGroup(t, want))
	raw, err := k.EpochGroupDataMap.Get(ctx, collections.Join(want.EpochIndex, want.ModelId))
	require.NoError(t, err)
	require.False(t, raw.DefaultTimeslotAllocations)
}
