package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	"github.com/cosmos/cosmos-sdk/runtime"

	"github.com/productscience/inference/x/genesistransfer/types"
)

// GetParams get all parameters as types.Params
func (k Keeper) GetParams(ctx context.Context) (params types.Params) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bz := store.Get(types.ParamsKey)
	if bz == nil {
		return types.DefaultParams()
	}

	k.cdc.MustUnmarshal(bz, &params)
	// Ensure AllowedAccounts is never nil after unmarshaling
	if params.AllowedAccounts == nil {
		params.AllowedAccounts = []string{}
	}
	return params
}

// SetParams set the params
func (k Keeper) SetParams(ctx context.Context, params types.Params) error {
	// Validate parameters before setting
	if err := params.Validate(); err != nil {
		return errorsmod.Wrapf(err, "invalid parameters")
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	bz, err := k.cdc.Marshal(&params)
	if err != nil {
		return errorsmod.Wrapf(err, "failed to marshal parameters")
	}
	store.Set(types.ParamsKey, bz)

	// Log parameter update
	k.Logger().Info(
		"module parameters updated",
		"allowed_accounts_count", len(params.AllowedAccounts),
		"restrict_to_list", params.RestrictToList,
	)

	return nil
}

// GetAllowedAccounts returns the list of allowed accounts for transfer
func (k Keeper) GetAllowedAccounts(ctx context.Context) []string {
	params := k.GetParams(ctx)
	return params.AllowedAccounts
}

// GetRestrictToList returns whether transfers are restricted to the allowed accounts list
func (k Keeper) GetRestrictToList(ctx context.Context) bool {
	params := k.GetParams(ctx)
	return params.RestrictToList
}
