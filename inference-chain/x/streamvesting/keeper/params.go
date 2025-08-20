package keeper

import (
	"context"

	"github.com/productscience/inference/x/streamvesting/types"
)

// GetParams get all parameters as types.Params
func (k Keeper) GetParams(ctx context.Context) (params types.Params) {
	v, err := k.params.Get(ctx)
	if err != nil {
		return params
	}
	return v
}

// SetParams set the params
func (k Keeper) SetParams(ctx context.Context, params types.Params) error {
	return k.params.Set(ctx, params)
}
