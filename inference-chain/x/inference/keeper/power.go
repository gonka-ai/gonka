package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetPower(ctx context.Context, power types.Power) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.PowerKeyPrefix))
	key := types.PowerKey(power.ParticipantAddress)

	b := k.cdc.MustMarshal(&power)
	store.Set(key, b)
}
