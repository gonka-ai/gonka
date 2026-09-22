package keeper

import "context"

// SetPocSchemeEnabledEpoch records the epoch of the latest poc_scheme change.
// Overwritten on every PREFILL↔DECODE flip so rollback also gets a grace epoch.
func (k Keeper) SetPocSchemeEnabledEpoch(ctx context.Context, epoch uint64) error {
	return k.PocSchemeEnabledEpoch.Set(ctx, epoch)
}

func (k Keeper) GetPocSchemeEnabledEpoch(ctx context.Context) (uint64, bool) {
	epoch, err := k.PocSchemeEnabledEpoch.Get(ctx)
	if err != nil {
		return 0, false
	}
	return epoch, true
}
