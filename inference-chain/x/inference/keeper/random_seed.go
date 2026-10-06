package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetRandomSeed(ctx context.Context, seed types.RandomSeed) error {
	addr, err := sdk.AccAddressFromBech32(seed.Participant)
	if err != nil {
		return err
	}
	pk := collections.Join(seed.EpochIndex, addr)
	if err := k.RandomSeeds.Set(ctx, pk, storedRandomSeed(seed, addr)); err != nil {
		return err
	}
	return nil
}

// storedRandomSeed drops the epoch and participant the key holds; restoredRandomSeed fills them back.
// The full record is kept when the key would not restore the same address string.
func storedRandomSeed(seed types.RandomSeed, participant sdk.AccAddress) types.RandomSeed {
	if participant.String() != seed.Participant || seed.Signature == "" {
		return seed
	}
	return types.RandomSeed{Signature: seed.Signature}
}

func restoredRandomSeed(key collections.Pair[uint64, sdk.AccAddress], seed types.RandomSeed) types.RandomSeed {
	if seed.Participant == "" {
		seed.Participant, seed.EpochIndex = key.K2().String(), key.K1()
	}
	return seed
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
	return restoredRandomSeed(pk, v), true
}
