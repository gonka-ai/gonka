package inference

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"cosmossdk.io/core/header"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/calculations"
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

type countingUpgradePlan struct {
	plan  upgradetypes.Plan
	calls int
}

func (c *countingUpgradePlan) GetUpgradePlan(context.Context) (upgradetypes.Plan, error) {
	c.calls++
	return c.plan, nil
}

// The trigger check reads upgrade protection state only on blocks that won the draw,
// and an upgrade in the window still blocks the event.
func TestConfirmationPoCTriggerReadsUpgradeStateOnlyAfterDraw(t *testing.T) {
	for _, tc := range []struct {
		name      string
		win       bool
		upgrade   bool
		wantReads int
		wantEvent bool
	}{
		{"lost draw", false, false, 0, false},
		{"won draw, no upgrade", true, false, 1, true},
		{"won draw, upgrade in window", true, true, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx := newMinimalInferenceKeeper(t)
			params, err := k.GetParams(ctx)
			require.NoError(t, err)
			ep := params.EpochParams
			ep.EpochLength = 2000
			ep.PocStageDuration, ep.PocExchangeDuration = 20, 5
			ep.PocValidationDelay, ep.PocValidationDuration = 2, 10
			ep.SetNewValidatorsDelay, ep.InferenceValidationCutoff, ep.ConfirmationPocSafetyWindow = 1, 5, 10
			epoch := types.Epoch{Index: 5, PocStartBlockHeight: 10000}
			ec := types.NewEpochContext(epoch, *ep)
			height := ec.SetNewValidators() + 1
			windowEnd := ec.NextEpochContext().PocStartBlockHeight - ep.InferenceValidationCutoff -
				(ep.PocStageDuration + ep.PocExchangeDuration + ep.PocValidationDelay + ep.PocValidationDuration +
					ep.SetNewValidatorsDelay + ep.ConfirmationPocSafetyWindow)
			windowLen := windowEnd - ec.SetNewValidators() + 1
			require.Greater(t, windowLen, int64(1))

			cp := *params.ConfirmationPocParams
			hash := make([]byte, 32)
			if tc.win {
				cp.ExpectedConfirmationsPerEpoch = uint64(windowLen) // probability 1
			} else {
				cp.ExpectedConfirmationsPerEpoch = 1
				p := decimal.NewFromInt(1).Div(decimal.NewFromInt(windowLen))
				for seed := uint64(1); ; seed++ {
					binary.BigEndian.PutUint64(hash, seed)
					r := calculations.DeterministicFloat(int64(seed), fmt.Sprintf("confirmation_poc_trigger_%d", height))
					if !r.LessThan(p) {
						break
					}
				}
			}
			up := &countingUpgradePlan{}
			if tc.upgrade {
				up.plan = upgradetypes.Plan{Name: "v", Height: height + 10}
			}
			k.UpgradeKeeper = up
			blockCtx := ctx.WithBlockHeight(height).WithHeaderInfo(header.Info{Height: height, Hash: hash})

			am := NewAppModule(nil, k, nil, nil, nil, nil)
			err = am.checkConfirmationPoCTrigger(blockCtx, height, &ec, ep, &cp, blockCtx,
				func() (bool, error) { return false, nil })
			require.NoError(t, err)
			require.Equal(t, tc.wantReads, up.calls, "GetUpgradePlan calls")
			_, found, err := k.GetActiveConfirmationPoCEvent(blockCtx)
			require.NoError(t, err)
			require.Equal(t, tc.wantEvent, found, "event created")
		})
	}
}
