package inference

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

// BeginBlock must not touch epoch group data: nothing in the block reads a per-block copy of it.
func TestBeginBlockDoesNotReadEpochGroupData(t *testing.T) {
	traceBeginBlock := func(withGroups bool) int {
		k, ctx := newMinimalInferenceKeeper(t)
		require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
		if withGroups {
			for _, epoch := range []uint64{4, 5} {
				k.SetEpochGroupData(ctx, types.EpochGroupData{
					EpochIndex:        epoch,
					SubGroupModels:    []string{"model-a"},
					ValidationWeights: []*types.ValidationWeight{{MemberAddress: "val1", Weight: 10}},
				})
				k.SetEpochGroupData(ctx, types.EpochGroupData{
					EpochIndex:        epoch,
					ModelId:           "model-a",
					ValidationWeights: []*types.ValidationWeight{{MemberAddress: "val1", Weight: 10}},
				})
			}
		}
		var trace bytes.Buffer
		ctx.MultiStore().SetTracer(&trace)
		require.NoError(t, NewAppModule(nil, k, nil, nil, nil, nil).BeginBlock(ctx))
		return bytes.Count(trace.Bytes(), []byte("\n"))
	}
	require.Equal(t, traceBeginBlock(false), traceBeginBlock(true))
}
