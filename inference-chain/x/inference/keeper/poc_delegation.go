package keeper

import (
	"context"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

// --- PoCDelegation ---

func (k Keeper) SetPoCDelegation(ctx context.Context, d types.PoCDelegation) error {
	return k.PoCDelegations.Set(ctx, collections.Join(d.ModelId, d.Delegator), d)
}

func (k Keeper) GetPoCDelegation(ctx context.Context, modelID, delegator string) (types.PoCDelegation, bool) {
	v, err := k.PoCDelegations.Get(ctx, collections.Join(modelID, delegator))
	if err != nil {
		return types.PoCDelegation{}, false
	}
	return v, true
}

func (k Keeper) DeletePoCDelegation(ctx context.Context, modelID, delegator string) error {
	return k.PoCDelegations.Remove(ctx, collections.Join(modelID, delegator))
}

func (k Keeper) GetPoCDelegationsForModel(ctx context.Context, modelID string) ([]types.PoCDelegation, error) {
	rng := collections.NewPrefixedPairRange[string, string](modelID)
	iter, err := k.PoCDelegations.Iterate(ctx, rng)
	if err != nil {
		return nil, err
	}
	vals, err := iter.Values()
	if err != nil {
		return nil, err
	}
	result := make([]types.PoCDelegation, len(vals))
	copy(result, vals)
	return result, nil
}

func (k Keeper) GetAllPoCDelegations(ctx context.Context) ([]types.PoCDelegation, error) {
	iter, err := k.PoCDelegations.Iterate(ctx, nil)
	if err != nil {
		return nil, err
	}
	vals, err := iter.Values()
	if err != nil {
		return nil, err
	}
	result := make([]types.PoCDelegation, len(vals))
	copy(result, vals)
	return result, nil
}

func (k Keeper) GetPoCDelegationsForParticipant(ctx context.Context, participant string) ([]types.PoCDelegation, error) {
	all, err := k.GetAllPoCDelegations(ctx)
	if err != nil {
		return nil, err
	}
	var result []types.PoCDelegation
	for _, d := range all {
		if d.Delegator == participant {
			result = append(result, d)
		}
	}
	return result, nil
}

// --- PoCRefusal ---

func (k Keeper) SetPoCRefusal(ctx context.Context, modelID, participant string) error {
	return k.PoCRefusals.Set(ctx, collections.Join(modelID, participant))
}

func (k Keeper) HasPoCRefusal(ctx context.Context, modelID, participant string) bool {
	has, _ := k.PoCRefusals.Has(ctx, collections.Join(modelID, participant))
	return has
}

func (k Keeper) DeletePoCRefusal(ctx context.Context, modelID, participant string) error {
	return k.PoCRefusals.Remove(ctx, collections.Join(modelID, participant))
}

func (k Keeper) DeleteAllPoCRefusals(ctx context.Context) error {
	return k.PoCRefusals.Clear(ctx, nil)
}

func (k Keeper) GetPoCRefusalsForParticipant(ctx context.Context, participant string) ([]types.PoCRefusal, error) {
	iter, err := k.PoCRefusals.Iterate(ctx, nil)
	if err != nil {
		return nil, err
	}
	keys, err := iter.Keys()
	if err != nil {
		return nil, err
	}
	var result []types.PoCRefusal
	for _, key := range keys {
		if key.K2() == participant {
			result = append(result, types.PoCRefusal{ModelId: key.K1(), Participant: key.K2()})
		}
	}
	return result, nil
}

// --- PoCDirectIntent ---

func (k Keeper) SetPoCDirectIntent(ctx context.Context, modelID, participant string) error {
	return k.PoCDirectIntents.Set(ctx, collections.Join(modelID, participant))
}

func (k Keeper) HasPoCDirectIntent(ctx context.Context, modelID, participant string) bool {
	has, _ := k.PoCDirectIntents.Has(ctx, collections.Join(modelID, participant))
	return has
}

func (k Keeper) DeletePoCDirectIntent(ctx context.Context, modelID, participant string) error {
	return k.PoCDirectIntents.Remove(ctx, collections.Join(modelID, participant))
}

func (k Keeper) DeleteAllPoCDirectIntents(ctx context.Context) error {
	return k.PoCDirectIntents.Clear(ctx, nil)
}

func (k Keeper) GetPoCDirectIntentsForParticipant(ctx context.Context, participant string) ([]types.PoCDirectIntent, error) {
	iter, err := k.PoCDirectIntents.Iterate(ctx, nil)
	if err != nil {
		return nil, err
	}
	keys, err := iter.Keys()
	if err != nil {
		return nil, err
	}
	var result []types.PoCDirectIntent
	for _, key := range keys {
		if key.K2() == participant {
			result = append(result, types.PoCDirectIntent{ModelId: key.K1(), Participant: key.K2()})
		}
	}
	return result, nil
}

// --- Last-write-wins clearing ---

// --- DelegationSnapshot ---

func (k Keeper) SetDelegationSnapshot(ctx context.Context, snapshot types.DelegationSnapshot) error {
	return k.DelegationSnapshot.Set(ctx, snapshot)
}

func (k Keeper) GetDelegationSnapshot(ctx context.Context) (types.DelegationSnapshot, bool) {
	snapshot, err := k.DelegationSnapshot.Get(ctx)
	if err != nil {
		return types.DelegationSnapshot{}, false
	}
	return snapshot, true
}

func (k Keeper) DeleteDelegationSnapshot(ctx context.Context) error {
	return k.DelegationSnapshot.Remove(ctx)
}

// --- BootstrapDelegationSnapshot ---

func (k Keeper) SetBootstrapDelegationSnapshot(ctx context.Context, snapshot types.BootstrapDelegationSnapshot) error {
	return k.BootstrapDelegationSnapshot.Set(ctx, snapshot)
}

func (k Keeper) GetBootstrapDelegationSnapshot(ctx context.Context) (types.BootstrapDelegationSnapshot, bool) {
	snapshot, err := k.BootstrapDelegationSnapshot.Get(ctx)
	if err != nil {
		return types.BootstrapDelegationSnapshot{}, false
	}
	return snapshot, true
}

func (k Keeper) DeleteBootstrapDelegationSnapshot(ctx context.Context) error {
	return k.BootstrapDelegationSnapshot.Remove(ctx)
}

// --- Last-write-wins clearing ---

// ClearOtherDelegationState removes delegation state for the given (modelID, participant)
// except the type indicated by keep ("delegation", "refusal", or "intent").
func (k Keeper) ClearOtherDelegationState(ctx context.Context, modelID, participant, keep string) {
	if keep != "delegation" {
		_ = k.DeletePoCDelegation(ctx, modelID, participant)
	}
	if keep != "refusal" {
		_ = k.DeletePoCRefusal(ctx, modelID, participant)
	}
	if keep != "intent" {
		_ = k.DeletePoCDirectIntent(ctx, modelID, participant)
	}
}
