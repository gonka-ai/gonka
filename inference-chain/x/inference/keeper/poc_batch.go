package keeper

import (
	"context"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrtypes "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/x/inference/types"
)

// SetPocBatch stores a PoCBatch under triple key (epoch, participant addr, batch_id)
func (k Keeper) SetPocBatch(ctx context.Context, batch types.PoCBatch) error {
	addr, err := sdk.AccAddressFromBech32(batch.ParticipantAddress)
	if err != nil {
		return err
	}
	pk := collections.Join3(batch.PocStageStartBlockHeight, addr, batch.BatchId)
	k.LogInfo("PoC: Storing batch", types.PoC, "epoch", batch.PocStageStartBlockHeight, "participant", batch.ParticipantAddress, "batch_id", batch.BatchId)
	if err := k.PoCBatches.Set(ctx, pk, batch); err != nil {
		return err
	}
	return nil
}

// GetPoCBatchesByStage collects all PoCBatch grouped by participant for a specific epoch
func (k Keeper) GetPoCBatchesByStage(ctx context.Context, pocStageStartBlockHeight int64) (map[string][]types.PoCBatch, error) {
	it, err := k.PoCBatches.Iterate(ctx, collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](pocStageStartBlockHeight))
	if err != nil {
		return nil, err
	}
	defer it.Close()

	batches := make(map[string][]types.PoCBatch)
	for ; it.Valid(); it.Next() {
		v, err := it.Value()
		if err != nil {
			return nil, err
		}
		batches[v.ParticipantAddress] = append(batches[v.ParticipantAddress], v)
	}
	return batches, nil
}

func (k Keeper) GetPoCBatchesCountByStage(ctx context.Context, pocStageStartBlockHeight int64) (uint64, error) {
	it, err := k.PoCBatches.Iterate(ctx, collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](pocStageStartBlockHeight))
	if err != nil {
		return 0, err
	}
	defer it.Close()
	var count uint64
	for ; it.Valid(); it.Next() {
		count++
	}
	return count, nil
}

// HasPoCValidation checks if a validation already exists for the given key combination.
// Returns true if a validation exists, false otherwise.
func (k Keeper) HasPoCValidation(ctx context.Context, pocStageStartBlockHeight int64, participantAddress string, validatorAddress string) (bool, error) {
	pAddr, err := sdk.AccAddressFromBech32(participantAddress)
	if err != nil {
		return false, errorsmod.Wrapf(sdkerrtypes.ErrInvalidAddress, "invalid participant address (%s)", err)
	}
	vAddr, err := sdk.AccAddressFromBech32(validatorAddress)
	if err != nil {
		return false, errorsmod.Wrapf(sdkerrtypes.ErrInvalidAddress, "invalid validator address (%s)", err)
	}
	pk := collections.Join3(pocStageStartBlockHeight, pAddr, vAddr)
	return k.PoCValidations.Has(ctx, pk)
}

func (k Keeper) SetPoCValidation(ctx context.Context, validation types.PoCValidation) error {
	pAddr, err := sdk.AccAddressFromBech32(validation.ParticipantAddress)
	if err != nil {
		return errorsmod.Wrapf(err, "invalid participant address (%s)", err)
	}
	vAddr, err := sdk.AccAddressFromBech32(validation.ValidatorParticipantAddress)
	if err != nil {
		return errorsmod.Wrapf(err, "invalid validator address (%s)", err)
	}
	pk := collections.Join3(validation.PocStageStartBlockHeight, pAddr, vAddr)
	k.LogInfo("PoC: Storing validation", types.PoC, "epoch", validation.PocStageStartBlockHeight, "participant", validation.ParticipantAddress, "validator", validation.ValidatorParticipantAddress)
	if err := k.PoCValidations.Set(ctx, pk, validation); err != nil {
		return err
	}
	return nil
}

func (k Keeper) GetPoCValidationByStage(ctx context.Context, pocStageStartBlockHeight int64) (map[string][]types.PoCValidation, error) {
	it, err := k.PoCValidations.Iterate(ctx, collections.NewPrefixedTripleRange[int64, sdk.AccAddress, sdk.AccAddress](pocStageStartBlockHeight))
	if err != nil {
		return nil, err
	}
	defer it.Close()
	validations := make(map[string][]types.PoCValidation)
	for ; it.Valid(); it.Next() {
		v, err := it.Value()
		if err != nil {
			return nil, err
		}
		validations[v.ParticipantAddress] = append(validations[v.ParticipantAddress], v)
	}
	return validations, nil
}

func (k Keeper) GetPocValidationCountByStage(ctx context.Context, pocStageStartBlockHeight int64) (uint64, error) {
	it, err := k.PoCValidations.Iterate(ctx, collections.NewPrefixedTripleRange[int64, sdk.AccAddress, sdk.AccAddress](pocStageStartBlockHeight))
	if err != nil {
		return 0, err
	}
	defer it.Close()
	var count uint64
	for ; it.Valid(); it.Next() {
		count++
	}
	return count, nil

}
