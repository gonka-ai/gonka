package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"

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

// AddAllowedAccount adds an account to the allowed accounts list
func (k Keeper) AddAllowedAccount(ctx context.Context, address string) error {
	// Validate the address
	if _, err := sdk.AccAddressFromBech32(address); err != nil {
		return errorsmod.Wrapf(err, "invalid address: %s", address)
	}

	params := k.GetParams(ctx)

	// Check if already exists
	for _, addr := range params.AllowedAccounts {
		if addr == address {
			return errorsmod.Wrapf(types.ErrInvalidTransfer, "address %s already in allowed list", address)
		}
	}

	// Add the address
	params.AllowedAccounts = append(params.AllowedAccounts, address)

	return k.SetParams(ctx, params)
}

// RemoveAllowedAccount removes an account from the allowed accounts list
func (k Keeper) RemoveAllowedAccount(ctx context.Context, address string) error {
	params := k.GetParams(ctx)

	// Find and remove the address
	for i, addr := range params.AllowedAccounts {
		if addr == address {
			// Remove by creating new slice
			params.AllowedAccounts = append(params.AllowedAccounts[:i], params.AllowedAccounts[i+1:]...)
			return k.SetParams(ctx, params)
		}
	}

	return errorsmod.Wrapf(types.ErrAccountNotFound, "address %s not found in allowed list", address)
}

// SetRestrictToList sets whether transfers should be restricted to the allowed accounts list
func (k Keeper) SetRestrictToList(ctx context.Context, restrict bool) error {
	params := k.GetParams(ctx)
	params.RestrictToList = restrict
	return k.SetParams(ctx, params)
}

// ValidateParams validates module parameters
func (k Keeper) ValidateParams(ctx context.Context, params types.Params) error {
	return params.Validate()
}

// ResetParams resets parameters to default values
func (k Keeper) ResetParams(ctx context.Context) error {
	defaultParams := types.DefaultParams()
	return k.SetParams(ctx, defaultParams)
}
