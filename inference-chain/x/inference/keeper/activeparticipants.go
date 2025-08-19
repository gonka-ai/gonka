package keeper

import (
	"context"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetActiveParticipantsV1(ctx context.Context, participants types.ActiveParticipants) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	key := types.ActiveParticipantsFullKeyV1(participants.EpochGroupId)

	b := k.cdc.MustMarshal(&participants)
	store.Set(key, b)
}

func (k Keeper) GetActiveParticipants(ctx context.Context, epochId uint64) (val types.ActiveParticipants, found bool) {
	v, err := k.ActiveParticipantsMap.Get(ctx, epochId)
	if err != nil {
		return types.ActiveParticipants{}, false
	}
	return v, true
}

func (k Keeper) SetActiveParticipants(ctx context.Context, participants types.ActiveParticipants) {
	_ = k.ActiveParticipantsMap.Set(ctx, participants.EpochId, participants)
}
