package inference

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

type noUpgradePlan struct{}

func (noUpgradePlan) GetUpgradePlan(context.Context) (upgradetypes.Plan, error) {
	return upgradetypes.Plan{}, nil
}

// EndBlock reads params, the effective epoch and the active confirmation PoC event once each.
func TestEndBlockConfirmationPoCReadsEachKeyOnce(t *testing.T) {
	for _, withEvent := range []bool{false, true} {
		k, ctx := newMinimalInferenceKeeper(t)
		k.UpgradeKeeper = noUpgradePlan{}
		params, err := k.GetParams(ctx)
		require.NoError(t, err)
		params.ConfirmationPocParams.ExpectedConfirmationsPerEpoch = 1
		require.NoError(t, k.SetParams(ctx, params))
		epoch := types.Epoch{Index: 5, PocStartBlockHeight: 1000}
		require.NoError(t, k.SetEpoch(ctx, &epoch))
		require.NoError(t, k.SetEffectiveEpochIndex(ctx, epoch.Index))
		ec := types.NewEpochContext(epoch, *params.EpochParams)
		height := ec.SetNewValidators() + 1
		if withEvent {
			require.NoError(t, k.SetActiveConfirmationPoCEvent(ctx, types.ConfirmationPoCEvent{
				EpochIndex:            epoch.Index,
				TriggerHeight:         height - 2,
				GenerationStartHeight: height - 1,
				Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
			}))
		}
		blockCtx := ctx.WithBlockHeight(height)

		var trace bytes.Buffer
		blockCtx.MultiStore().SetTracer(&trace)
		_ = NewAppModule(nil, k, nil, nil, nil, nil).EndBlock(blockCtx)
		blockCtx.MultiStore().SetTracer(nil)

		reads := func(key []byte) int {
			enc := base64.StdEncoding.EncodeToString(key)
			n := 0
			for _, line := range strings.Split(trace.String(), "\n") {
				if strings.Contains(line, `"operation":"read"`) && strings.Contains(line, `"key":"`+enc+`"`) {
					n++
				}
			}
			return n
		}
		require.Equal(t, 1, reads(types.ParamsKey), "params, withEvent=%v", withEvent)
		require.Equal(t, 1, reads(types.EffectiveEpochIndexPrefix), "effective epoch index, withEvent=%v", withEvent)
		require.Equal(t, 1, reads(types.ActiveConfirmationPoCEventPrefix), "active event, withEvent=%v", withEvent)
	}
}
