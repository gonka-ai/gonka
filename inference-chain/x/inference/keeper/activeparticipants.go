package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetActiveParticipantsV1(ctx context.Context, participants types.ActiveParticipants) error {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	key := types.ActiveParticipantsFullKeyV1(participants.EpochGroupId)

	b, err := k.cdc.Marshal(&participants)
	if err != nil {
		return err
	}
	store.Set(key, b)
	return nil
}

func (k Keeper) GetActiveParticipants(ctx context.Context, epochId uint64) (val types.ActiveParticipants, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	key := types.ActiveParticipantsFullKey(epochId)

	b := store.Get(key)
	if b == nil {
		return types.ActiveParticipants{}, false
	}

	err := k.cdc.Unmarshal(b, &val)
	if err != nil {
		k.LogError("Unable to marshal ActiveParticipants", types.Participants, "epochIndex", epochId)
		return types.ActiveParticipants{}, false
	}
	return val, true
}

func (k Keeper) SetActiveParticipants(ctx context.Context, participants types.ActiveParticipants) error {
	err := k.SetActiveParticipantsCache(ctx, participants)
	if err != nil {
		return err
	}

	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte{})

	key := types.ActiveParticipantsFullKey(participants.EpochId)

	b, err := k.cdc.Marshal(&participants)
	if err != nil {
		return err
	}
	store.Set(key, b)
	return nil
}

func (k Keeper) SetActiveParticipantsCache(ctx context.Context, participants types.ActiveParticipants) error {
	r := collections.NewPrefixedPairRange[uint64, sdk.AccAddress](participants.EpochId)
	err := k.ActiveParticipantsSet.Clear(ctx, r)
	if err != nil {
		return err
	}
	for _, p := range participants.Participants {
		addr, err := sdk.AccAddressFromBech32(p.Index)
		if err != nil {
			return err
		}
		err = k.ActiveParticipantsSet.Set(ctx, collections.Join(participants.EpochId, addr))
		if err != nil {
			return err
		}
	}
	return nil
}

func (k Keeper) ReactivateActiveParticipants(ctx context.Context, epochIndex uint64) error {
	activeParticipants, found := k.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return types.ErrActiveParticipantNotFound
	}

	ids := make([]string, len(activeParticipants.Participants))
	for i, participant := range activeParticipants.Participants {
		ids[i] = participant.Index
	}

	for _, participant := range k.GetParticipants(ctx, ids) {
		participant.Status = types.ParticipantStatus_ACTIVE
		participant.ConsecutiveInvalidInferences = 0
		if err := k.SetParticipant(ctx, participant); err != nil {
			return err
		}
	}

	return nil
}
