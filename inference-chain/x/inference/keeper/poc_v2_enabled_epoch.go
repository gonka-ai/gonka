package keeper

import "context"

// SetPocV2EnabledEpoch stores the epoch when poc_v2_enabled was first set to true.
// This is used for the migration grace period - confirmation PoC in this epoch runs in dry-run mode.
func (k Keeper) SetPocV2EnabledEpoch(ctx context.Context, epoch uint64) error {
	return k.PocV2EnabledEpoch.Set(ctx, epoch)
}

// GetPocV2EnabledEpoch returns the epoch when poc_v2_enabled was first set to true.
// Returns (0, false) if not set.
func (k Keeper) GetPocV2EnabledEpoch(ctx context.Context) (uint64, bool) {
	epoch, err := k.PocV2EnabledEpoch.Get(ctx)
	if err != nil {
		return 0, false
	}
	return epoch, true
}
