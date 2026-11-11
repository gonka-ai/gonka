package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func ResolveTrustWeights(participants []*types.ActiveParticipant, capApplied bool) map[string]int64 {
	useCap := capApplied
	if !useCap {
		for _, p := range participants {
			if p != nil && p.CapWeight > 0 {
				useCap = true
				break
			}
		}
	}
	weights := make(map[string]int64, len(participants))
	for _, p := range participants {
		if p == nil {
			continue
		}
		if useCap {
			weights[p.Index] = p.CapWeight
		} else {
			weights[p.Index] = p.Weight
		}
	}
	return weights
}

func (k Keeper) admittedEpochMembers(ctx context.Context, epochIndex uint64) (map[string]struct{}, error) {
	epochGroup, err := k.GetEpochGroup(ctx, epochIndex, "")
	if err != nil {
		return nil, err
	}
	members, err := epochGroup.GetGroupMembers(ctx)
	if err != nil {
		return nil, err
	}
	admitted := make(map[string]struct{}, len(members))
	for _, member := range members {
		if member == nil || member.Member == nil {
			continue
		}
		addr, err := sdk.AccAddressFromBech32(member.Member.Address)
		if err != nil {
			continue
		}
		admitted[addr.String()] = struct{}{}
	}
	iter, err := k.ExcludedParticipantsMap.Iterate(ctx, collections.NewPrefixedPairRange[uint64, sdk.AccAddress](epochIndex))
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return nil, err
		}
		admitted[key.K2().String()] = struct{}{}
	}
	return admitted, nil
}

func admittedTrustWeights(participants types.ActiveParticipants, admitted map[string]struct{}) ([]sdk.AccAddress, []int64) {
	resolved := ResolveTrustWeights(participants.Participants, participants.CapWeightApplied)
	seen := make(map[string]struct{}, len(participants.Participants))
	var addrs []sdk.AccAddress
	var weights []int64
	for _, p := range participants.Participants {
		if p == nil {
			continue
		}
		weight := resolved[p.Index]
		if weight <= 0 {
			continue
		}
		addr, err := sdk.AccAddressFromBech32(p.Index)
		if err != nil {
			continue
		}
		if _, ok := admitted[addr.String()]; !ok {
			continue
		}
		if _, dup := seen[addr.String()]; dup {
			continue
		}
		seen[addr.String()] = struct{}{}
		addrs = append(addrs, addr)
		weights = append(weights, weight)
	}
	return addrs, weights
}

func (k Keeper) ResetEpochTrustWeights(ctx context.Context, epochIndex uint64) error {
	if err := k.ClearEpochTrustWeights(ctx, epochIndex); err != nil {
		return err
	}
	return k.EpochTrustWeightTotals.Set(ctx, epochIndex, 0)
}

func (k Keeper) AddEpochTrustWeight(ctx context.Context, epochIndex uint64, participant string, weight int64) error {
	if weight <= 0 {
		return nil
	}
	addr, err := sdk.AccAddressFromBech32(participant)
	if err != nil {
		return err
	}
	key := collections.Join(epochIndex, addr)
	exists, err := k.EpochTrustWeights.Has(ctx, key)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	total, err := k.EpochTrustWeightTotals.Get(ctx, epochIndex)
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return err
	}
	if err := k.EpochTrustWeights.Set(ctx, key, weight); err != nil {
		return err
	}
	return k.EpochTrustWeightTotals.Set(ctx, epochIndex, total+weight)
}

func (k Keeper) SetEpochTrustWeights(ctx context.Context, participants types.ActiveParticipants) error {
	admitted, err := k.admittedEpochMembers(ctx, participants.EpochId)
	if err != nil {
		return err
	}
	if err := k.ResetEpochTrustWeights(ctx, participants.EpochId); err != nil {
		return err
	}
	addrs, weights := admittedTrustWeights(participants, admitted)
	for i, addr := range addrs {
		if err := k.AddEpochTrustWeight(ctx, participants.EpochId, addr.String(), weights[i]); err != nil {
			return err
		}
	}
	return nil
}

func (k Keeper) ClearEpochTrustWeights(ctx context.Context, epochIndex uint64) error {
	rng := collections.NewPrefixedPairRange[uint64, sdk.AccAddress](epochIndex)
	if err := k.EpochTrustWeights.Clear(ctx, rng); err != nil {
		return err
	}
	return k.EpochTrustWeightTotals.Remove(ctx, epochIndex)
}

type EpochTrust struct {
	Total   int64
	weights map[string]int64
	indexed bool
	epoch   uint64
}

func (k Keeper) GetEpochTrust(ctx context.Context, epochIndex uint64) (EpochTrust, error) {
	total, err := k.EpochTrustWeightTotals.Get(ctx, epochIndex)
	if err == nil {
		return EpochTrust{Total: total, indexed: true, epoch: epochIndex}, nil
	}
	if !errors.Is(err, collections.ErrNotFound) {
		return EpochTrust{}, err
	}
	aps, found := k.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return EpochTrust{}, errorsmod.Wrapf(types.ErrActiveParticipantNotFound, "no trust weights for epoch %d", epochIndex)
	}
	admitted, err := k.admittedEpochMembers(ctx, epochIndex)
	if err != nil {
		return EpochTrust{}, errorsmod.Wrapf(types.ErrActiveParticipantNotFound, "no epoch group for epoch %d: %v", epochIndex, err)
	}
	addrs, list := admittedTrustWeights(aps, admitted)
	weights := make(map[string]int64, len(addrs))
	for i, addr := range addrs {
		weights[addr.String()] = list[i]
		total += list[i]
	}
	return EpochTrust{Total: total, weights: weights, epoch: epochIndex}, nil
}

func (k Keeper) EpochTrustWeight(ctx context.Context, trust EpochTrust, addr sdk.AccAddress) (int64, error) {
	if !trust.indexed {
		return trust.weights[addr.String()], nil
	}
	weight, err := k.EpochTrustWeights.Get(ctx, collections.Join(trust.epoch, addr))
	if errors.Is(err, collections.ErrNotFound) {
		return 0, nil
	}
	return weight, err
}
