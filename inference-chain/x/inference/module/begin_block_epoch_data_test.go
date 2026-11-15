package inference

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

// BeginBlock reads only the current root epoch group (pricing needs its model list), never a per-block copy of all groups.
func TestBeginBlockDoesNotReadEpochGroupData(t *testing.T) {
	traceBeginBlock := func(withGroups bool) int {
		k, ctx := newMinimalInferenceKeeper(t)
		require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
		// Pricing reads the current root group for its model list; nothing else may be read.
		k.SetEpochGroupData(ctx, types.EpochGroupData{
			EpochIndex:        5,
			SubGroupModels:    []string{"model-a"},
			ValidationWeights: []*types.ValidationWeight{{MemberAddress: "val1", Weight: 10}},
		})
		if withGroups {
			k.SetEpochGroupData(ctx, types.EpochGroupData{
				EpochIndex:        4,
				SubGroupModels:    []string{"model-a"},
				ValidationWeights: []*types.ValidationWeight{{MemberAddress: "val1", Weight: 10}},
			})
			for _, epoch := range []uint64{4, 5} {
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
