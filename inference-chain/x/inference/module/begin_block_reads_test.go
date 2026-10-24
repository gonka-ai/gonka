package inference

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

// BeginBlock reads params and the effective epoch index once each and never loads the epoch itself.
func TestBeginBlockReadsEachKeyOnce(t *testing.T) {
	for _, epochIndex := range []uint64{5, 200} { // inside and after the pricing grace period
		k, ctx := newMinimalInferenceKeeper(t)
		require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: epochIndex, PocStartBlockHeight: 1000}))
		require.NoError(t, k.SetEffectiveEpochIndex(ctx, epochIndex))
		k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: epochIndex, SubGroupModels: []string{"m1", "m2"}})
		require.NoError(t, k.CacheModelCapacity(ctx, "m1", 1000))
		require.NoError(t, k.CacheModelCapacity(ctx, "m2", 1000))
		blockCtx := ctx.WithBlockHeight(1100)

		var trace bytes.Buffer
		blockCtx.MultiStore().SetTracer(&trace)
		require.NoError(t, NewAppModule(nil, k, nil, nil, nil, nil).BeginBlock(blockCtx))
		blockCtx.MultiStore().SetTracer(nil)

		reads := func(key []byte) int {
			enc := base64.StdEncoding.EncodeToString(key)
			n := 0
			for _, line := range strings.Split(trace.String(), "\n") {
				if strings.Contains(line, `"operation":"read"`) && strings.Contains(line, `"key":"`+enc) {
					n++
				}
			}
			return n
		}
		require.Equal(t, 1, reads(types.ParamsKey), "params, epoch %d", epochIndex)
		require.Equal(t, 1, reads(types.EffectiveEpochIndexPrefix), "effective epoch index, epoch %d", epochIndex)
		require.Equal(t, 0, reads(types.EpochsPrefix), "epoch, epoch %d", epochIndex)

		params, err := k.GetParams(ctx)
		require.NoError(t, err)
		dp := params.DynamicPricingParams
		want := dp.BasePerTokenPrice // first priced block after grace: base price, then the step
		if epochIndex < dp.GracePeriodEndEpoch {
			want = dp.GracePeriodPerTokenPrice
		}
		price, err := k.GetModelCurrentPrice(blockCtx, "m1")
		require.NoError(t, err, "epoch %d", epochIndex)
		if epochIndex < dp.GracePeriodEndEpoch {
			require.Equal(t, want, price, "epoch %d", epochIndex)
		} else {
			require.LessOrEqual(t, price, want, "zero load lowers the price, epoch %d", epochIndex)
			require.GreaterOrEqual(t, price, dp.MinPerTokenPrice, "epoch %d", epochIndex)
		}
	}
}
