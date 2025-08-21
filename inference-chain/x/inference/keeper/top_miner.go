package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// SetTopMiner sets a specific TopMiner in the store, indexed by sdk.AccAddress
func (k Keeper) SetTopMiner(ctx context.Context, topMiner types.TopMiner) {
	addr, err := sdk.AccAddressFromBech32(topMiner.Address)
	if err != nil {
		panic(err)
	}
	if err := k.TopMiners.Set(ctx, addr, topMiner); err != nil {
		panic(err)
	}
}

// GetTopMiner returns a TopMiner by address (bech32)
func (k Keeper) GetTopMiner(
	ctx context.Context,
	address string,
) (val types.TopMiner, found bool) {
	addr, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		return types.TopMiner{}, false
	}
	v, err := k.TopMiners.Get(ctx, addr)
	if err != nil {
		return types.TopMiner{}, false
	}
	return v, true
}

// RemoveTopMiner removes a TopMiner from the store by address
func (k Keeper) RemoveTopMiner(
	ctx context.Context,
	address string,
) {
	addr, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		return
	}
	_ = k.TopMiners.Remove(ctx, addr)
}

// GetAllTopMiner returns all TopMiners deterministically
func (k Keeper) GetAllTopMiner(ctx context.Context) (list []types.TopMiner) {
	it, err := k.TopMiners.Iterate(ctx, nil)
	if err != nil {
		return nil
	}
	defer it.Close()
	for ; it.Valid(); it.Next() {
		v, err := it.Value()
		if err != nil {
			panic(err)
		}
		list = append(list, v)
	}
	return list
}
