package keeper_test

import (
	"fmt"
	"testing"

	storetypes "cosmossdk.io/store/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

var aggregateTestModels = []string{"model-a", "model-b", "model-c"}

func aggregateTestMembers(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("gonka1member%02d", i)
	}
	return out
}

func TestAggregateMLNodesFromModelSubgroups_ReadsOnlyItsEpoch(t *testing.T) {
	members := aggregateTestMembers(30)
	const target = uint64(40)

	run := func(pastEpochs uint64) (map[string]map[string][]*types.MLNodeInfo, storetypes.Gas) {
		k, ctx := keepertest.InferenceKeeper(t)
		var rootWeights []*types.ValidationWeight
		for epoch := target - pastEpochs; epoch <= target+1; epoch++ {
			root := types.EpochGroupData{EpochIndex: epoch, SubGroupModels: aggregateTestModels}
			for _, m := range members {
				root.ValidationWeights = append(root.ValidationWeights, &types.ValidationWeight{MemberAddress: m, Weight: 100})
			}
			k.SetEpochGroupData(ctx, root)
			if epoch == target {
				rootWeights = root.ValidationWeights
			}
			for _, model := range aggregateTestModels {
				sub := types.EpochGroupData{EpochIndex: epoch, ModelId: model}
				for i, m := range members {
					sub.ValidationWeights = append(sub.ValidationWeights, &types.ValidationWeight{
						MemberAddress: m,
						Weight:        100,
						MlNodes: []*types.MLNodeInfo{
							{NodeId: fmt.Sprintf("%s-%d-%s-1", model, epoch, m), PocWeight: int64(i + 1)},
							{NodeId: fmt.Sprintf("%s-%d-%s-2", model, epoch, m), PocWeight: int64(i + 2)},
						},
					})
				}
				k.SetEpochGroupData(ctx, sub)
			}
		}
		ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		got := k.AggregateMLNodesFromModelSubgroups(ctx, target, rootWeights)
		return got, ctx.GasMeter().GasConsumed()
	}

	alone, gasAlone := run(0)
	withHistory, gasHistory := run(30)
	t.Logf("AggregateMLNodesFromModelSubgroups gas: 0 past epochs %d, 30 past epochs %d", gasAlone, gasHistory)

	require.Equal(t, alone, withHistory)
	require.Len(t, withHistory, len(members))
	for _, m := range members {
		require.Len(t, withHistory[m], len(aggregateTestModels))
		for _, model := range aggregateTestModels {
			nodes := withHistory[m][model]
			require.Len(t, nodes, 2)
			require.Equal(t, fmt.Sprintf("%s-%d-%s-1", model, target, m), nodes[0].NodeId)
		}
	}
	require.Equal(t, gasAlone, gasHistory, "gas must not grow with stored past epochs")
}
