package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

type ctxKeyParamsDraft struct{}

// paramsDraft holds params for the current tx. Used when paramsCacheMode == ParamsCacheModeTx.
type paramsDraft struct {
	p      *types.Params
	loaded bool
}

// WithParamsDraft attaches a new tx-scoped params draft to ctx. Call from AnteHandler at tx start.
// When present, GetParams/SetParams use the draft; CommitParamsDraftFromContext persists it on tx success.
func WithParamsDraft(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyParamsDraft{}, &paramsDraft{})
}

// getParamsDraftFromContext returns the tx-scoped draft, or nil if not in a params-draft tx.
func getParamsDraftFromContext(ctx context.Context) *paramsDraft {
	v := ctx.Value(ctxKeyParamsDraft{})
	if v == nil {
		return nil
	}
	d, _ := v.(*paramsDraft)
	return d
}

// CommitParamsDraftFromContext writes the tx-scoped params draft to store if it was changed this tx.
// Call from PostHandler on tx success. No-op if ctx has no draft or draft was never set.
// Applies PocV2 grace-epoch logic when committing (same as SetParams).
func (k Keeper) CommitParamsDraftFromContext(ctx context.Context) error {
	d := getParamsDraftFromContext(ctx)
	if d == nil || !d.loaded || d.p == nil {
		return nil
	}
	params := *d.p
	oldParams, _ := k.getParamsFromStore(ctx)
	if err := k.setParamsToStore(ctx, params); err != nil {
		return err
	}
	if params.PocParams != nil && params.PocParams.PocV2Enabled {
		wasV2Disabled := oldParams.PocParams == nil || !oldParams.PocParams.PocV2Enabled
		if wasV2Disabled {
			if _, exists := k.GetPocV2EnabledEpoch(ctx); !exists {
				if epoch, found := k.GetEffectiveEpochIndex(ctx); found {
					_ = k.SetPocV2EnabledEpoch(ctx, epoch)
				}
			}
		}
	}
	return nil
}

// InvalidateParamsBlockCache clears the per-block params cache. Call from BeginBlock when paramsCacheMode == ParamsCacheModeBlock.
func (k Keeper) InvalidateParamsBlockCache() {
	if k.paramsBlockCache == nil {
		return
	}
	k.paramsBlockCache.mu.Lock()
	defer k.paramsBlockCache.mu.Unlock()
	k.paramsBlockCache.p = nil
	k.paramsBlockCache.inited = false
}
