package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetRandomSeed(ctx context.Context, seed types.RandomSeed) {
	addr, err := sdk.AccAddressFromBech32(seed.Participant)
	if err != nil {
		panic(err)
	}
	pk := collections.Join(seed.EpochIndex, addr)
	if err := k.RandomSeeds.Set(ctx, pk, seed); err != nil {
		panic(err)
	}
}

func (k Keeper) GetRandomSeed(ctx context.Context, epochIndex uint64, participantAddress string) (types.RandomSeed, bool) {
	addr, err := sdk.AccAddressFromBech32(participantAddress)
	if err != nil {
		return types.RandomSeed{}, false
	}
	pk := collections.Join(epochIndex, addr)
	v, err := k.RandomSeeds.Get(ctx, pk)
	if err != nil {
		return types.RandomSeed{}, false
	}
	return v, true
}
