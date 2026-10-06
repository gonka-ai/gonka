package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func pocValidationV2Key(pocStageStartBlockHeight int64, participantAddr sdk.AccAddress, modelID string, validatorAddr sdk.AccAddress) collections.Triple[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]] {
	return collections.Join3(pocStageStartBlockHeight, participantAddr, collections.Join(modelID, validatorAddr))
}

func pocV2StoreCommitKey(pocStageStartBlockHeight int64, participantAddr sdk.AccAddress, modelID string) collections.Triple[int64, sdk.AccAddress, string] {
	return collections.Join3(pocStageStartBlockHeight, participantAddr, modelID)
}

// HasPocValidationV2 checks if a PoC v2 validation exists for the given key. Returns error on invalid addresses.
func (k Keeper) HasPocValidationV2(ctx context.Context, pocStageStartBlockHeight int64, participantAddress, modelID, validatorAddress string) (bool, error) {
	participantAddr, err := sdk.AccAddressFromBech32(participantAddress)
	if err != nil {
		return false, err
	}
	validatorAddr, err := sdk.AccAddressFromBech32(validatorAddress)
	if err != nil {
		return false, err
	}
	return k.PoCValidationsV2.Has(ctx, pocValidationV2Key(pocStageStartBlockHeight, participantAddr, modelID, validatorAddr))
}

// SetPocValidationV2 stores a PoC v2 validation. Returns error on invalid addresses or storage failure.
func (k Keeper) SetPocValidationV2(ctx context.Context, validation types.PoCValidationV2) error {
	participantAddr, err := sdk.AccAddressFromBech32(validation.ParticipantAddress)
	if err != nil {
		return err
	}
	validatorAddr, err := sdk.AccAddressFromBech32(validation.ValidatorParticipantAddress)
	if err != nil {
		return err
	}
	pk := pocValidationV2Key(validation.PocStageStartBlockHeight, participantAddr, validation.ModelId, validatorAddr)
	k.LogInfo("PoC v2: Storing validation", types.PoC,
		"epoch", validation.PocStageStartBlockHeight,
		"participant", validation.ParticipantAddress,
		"model_id", validation.ModelId,
		"validator", validation.ValidatorParticipantAddress,
		"validated_weight", validation.ValidatedWeight)
	return k.PoCValidationsV2.Set(ctx, pk, storedPocValidationV2(validation, participantAddr, validatorAddr))
}

// storedPocValidationV2 drops the fields the key already holds; GetPoCValidationsV2ByStage
// restores them. The full record is kept when the key would not restore the same strings
// (non-canonical bech32) or the value would be empty.
func storedPocValidationV2(v types.PoCValidationV2, participant, validator sdk.AccAddress) types.PoCValidationV2 {
	if v.PocStageStartBlockHeight == 0 && v.ValidatedWeight == 0 ||
		participant.String() != v.ParticipantAddress || validator.String() != v.ValidatorParticipantAddress {
		return v
	}
	return types.PoCValidationV2{PocStageStartBlockHeight: v.PocStageStartBlockHeight, ValidatedWeight: v.ValidatedWeight}
}

// GetPoCValidationsV2ByStage collects all PoCValidationV2 grouped by participant and model for a specific epoch.
func (k Keeper) GetPoCValidationsV2ByStage(ctx context.Context, pocStageStartBlockHeight int64) (map[types.PoCParticipantModelKey][]types.PoCValidationV2, error) {
	result := make(map[types.PoCParticipantModelKey][]types.PoCValidationV2)

	iter, err := k.PoCValidationsV2.Iterate(ctx, collections.NewPrefixedTripleRange[int64, sdk.AccAddress, collections.Pair[string, sdk.AccAddress]](pocStageStartBlockHeight))
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			return nil, err
		}
		validation := kv.Value
		if validation.ParticipantAddress == "" {
			validation.ParticipantAddress = kv.Key.K2().String()
			validation.ModelId = kv.Key.K3().K1()
			validation.ValidatorParticipantAddress = kv.Key.K3().K2().String()
			validation.PocStageStartBlockHeight = kv.Key.K1()
		}
		key := types.PoCParticipantModelKey{
			ParticipantAddress: validation.ParticipantAddress,
			ModelID:            validation.ModelId,
		}
		result[key] = append(result[key], validation)
	}

	return result, nil
}

// GetAllPoCV2StoreCommitsForStage returns all store commits for a given PoC stage, keyed by participant and model.
func (k Keeper) GetAllPoCV2StoreCommitsForStage(ctx context.Context, pocStageStartBlockHeight int64) (map[types.PoCParticipantModelKey]types.PoCV2StoreCommit, error) {
	result := make(map[types.PoCParticipantModelKey]types.PoCV2StoreCommit)

	iter, err := k.PoCV2StoreCommits.Iterate(ctx, collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](pocStageStartBlockHeight))
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			return nil, err
		}
		value := restoredPoCV2StoreCommit(kv.Key, kv.Value)
		result[types.PoCParticipantModelKey{
			ParticipantAddress: kv.Key.K2().String(),
			ModelID:            value.ModelId,
		}] = value
	}

	return result, nil
}

// GetAllMLNodeWeightDistributionsForStage returns all weight distributions for a given PoC stage, keyed by participant and model.
func (k Keeper) GetAllMLNodeWeightDistributionsForStage(ctx context.Context, pocStageStartBlockHeight int64) (map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution, error) {
	result := make(map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution)

	iter, err := k.MLNodeWeightDistributions.Iterate(ctx, collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](pocStageStartBlockHeight))
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			return nil, err
		}
		value := restoredMLNodeWeightDistribution(kv.Key, kv.Value)
		result[types.PoCParticipantModelKey{
			ParticipantAddress: kv.Key.K2().String(),
			ModelID:            value.ModelId,
		}] = value
	}

	return result, nil
}

// SetPoCV2StoreCommit stores a PoCV2StoreCommit (for testing). Returns error on invalid address or storage failure.
func (k Keeper) SetPoCV2StoreCommit(ctx context.Context, commit types.PoCV2StoreCommit) error {
	addr, err := sdk.AccAddressFromBech32(commit.ParticipantAddress)
	if err != nil {
		return err
	}
	pk := pocV2StoreCommitKey(commit.PocStageStartBlockHeight, addr, commit.ModelId)
	return k.PoCV2StoreCommits.Set(ctx, pk, storedPoCV2StoreCommit(commit, addr))
}

// SetMLNodeWeightDistribution stores an MLNodeWeightDistribution (for testing). Returns error on invalid address or storage failure.
func (k Keeper) SetMLNodeWeightDistribution(ctx context.Context, distribution types.MLNodeWeightDistribution) error {
	addr, err := sdk.AccAddressFromBech32(distribution.ParticipantAddress)
	if err != nil {
		return err
	}
	pk := pocV2StoreCommitKey(distribution.PocStageStartBlockHeight, addr, distribution.ModelId)
	return k.MLNodeWeightDistributions.Set(ctx, pk, storedMLNodeWeightDistribution(distribution, addr))
}

// storedPoCV2StoreCommit and storedMLNodeWeightDistribution drop the fields the key already
// holds; the restored* functions fill them back. The full record is kept when the key would
// not restore the same address string or the value would be empty.
func storedPoCV2StoreCommit(c types.PoCV2StoreCommit, participant sdk.AccAddress) types.PoCV2StoreCommit {
	if participant.String() != c.ParticipantAddress ||
		c.Count == 0 && len(c.RootHash) == 0 && c.CommitBlockHeight == 0 && c.TreeDepth == 0 {
		return c
	}
	c.ParticipantAddress, c.PocStageStartBlockHeight, c.ModelId = "", 0, ""
	return c
}

func restoredPoCV2StoreCommit(key collections.Triple[int64, sdk.AccAddress, string], c types.PoCV2StoreCommit) types.PoCV2StoreCommit {
	if c.ParticipantAddress == "" {
		c.ParticipantAddress, c.PocStageStartBlockHeight, c.ModelId = key.K2().String(), key.K1(), key.K3()
	}
	return c
}

func storedMLNodeWeightDistribution(d types.MLNodeWeightDistribution, participant sdk.AccAddress) types.MLNodeWeightDistribution {
	if participant.String() != d.ParticipantAddress || len(d.Weights) == 0 {
		return d
	}
	return types.MLNodeWeightDistribution{Weights: d.Weights}
}

func restoredMLNodeWeightDistribution(key collections.Triple[int64, sdk.AccAddress, string], d types.MLNodeWeightDistribution) types.MLNodeWeightDistribution {
	if d.ParticipantAddress == "" {
		d.ParticipantAddress, d.PocStageStartBlockHeight, d.ModelId = key.K2().String(), key.K1(), key.K3()
	}
	return d
}
