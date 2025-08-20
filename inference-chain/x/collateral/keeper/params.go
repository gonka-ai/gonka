package keeper

import (
	"context"

	"github.com/productscience/inference/x/collateral/types"
)

// GetParams get all parameters as types.Params
func (k Keeper) GetParams(ctx context.Context) types.Params {
	params, err := k.params.Get(ctx)
	if err != nil {
		panic(err)
	}
	return params
}

// SetParams set the params
func (k Keeper) SetParams(ctx context.Context, params types.Params) error {
	return k.params.Set(ctx, params)
}
