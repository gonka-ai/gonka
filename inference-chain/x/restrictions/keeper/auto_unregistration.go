package keeper

import (
	"strconv"

	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/restrictions/types"
)

// CheckAndUnregisterRestriction checks if the restriction deadline has passed
// and automatically unregisters the SendRestriction if needed
func (k Keeper) CheckAndUnregisterRestriction(ctx sdk.Context) error {
	// Check if restrictions are still active
	if k.IsRestrictionActive(ctx) {
		// Restrictions are still active, no action needed
		return nil
	}

	// Check if we've already unregistered (to avoid double processing)
	if k.isAlreadyUnregistered(ctx) {
		// Already unregistered, no action needed
		return nil
	}

	// Restrictions have expired, unregister the SendRestriction
	k.logger.Info("Transfer restrictions deadline reached, unregistering SendRestriction",
		"current_height", ctx.BlockHeight(),
		"restriction_end_block", k.GetParams(ctx).RestrictionEndBlock)

	// Emit event for restriction lifting
	params := k.GetParams(ctx)
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeRestrictionLifted,
			sdk.NewAttribute(types.AttributeKeyCurrentBlock, strconv.FormatInt(ctx.BlockHeight(), 10)),
			sdk.NewAttribute(types.AttributeKeyRestrictionEndBlock, strconv.FormatUint(params.RestrictionEndBlock, 10)),
		),
	)

	// Mark as unregistered to prevent repeated processing
	k.markAsUnregistered(ctx)

	return nil
}

// isAlreadyUnregistered checks if the SendRestriction has already been unregistered
func (k Keeper) isAlreadyUnregistered(ctx sdk.Context) bool {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	return store.Has(types.KeyRestrictionUnregistered)
}

// markAsUnregistered marks the SendRestriction as unregistered to prevent double processing
func (k Keeper) markAsUnregistered(ctx sdk.Context) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Set(types.KeyRestrictionUnregistered, []byte{1})
}
