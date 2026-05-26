package keeper

import (
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/types"
)

func teeValidationKey(stage int64, subject sdk.AccAddress, nodeID string, validator sdk.AccAddress) collections.Quad[int64, sdk.AccAddress, string, sdk.AccAddress] {
	return collections.Join4(stage, subject, nodeID, validator)
}

func (k Keeper) SetTeeValidation(ctx context.Context, v types.TeeValidation) error {
	subject, err := sdk.AccAddressFromBech32(v.SubjectAddress)
	if err != nil {
		return sdkerrors.Wrap(types.ErrInvalidAddress, fmt.Sprintf("invalid subject_address: %v", err))
	}
	validator, err := sdk.AccAddressFromBech32(v.ValidatorAddress)
	if err != nil {
		return sdkerrors.Wrap(types.ErrInvalidAddress, fmt.Sprintf("invalid validator_address: %v", err))
	}
	if v.NodeId == "" {
		return sdkerrors.Wrap(types.ErrTeeAttestationNodeIdEmpty, "node_id is empty")
	}
	return k.TeeValidations.Set(ctx, teeValidationKey(v.PocStageStartBlockHeight, subject, v.NodeId, validator), v)
}

func (k Keeper) HasTeeValidation(ctx context.Context, stage int64, subject sdk.AccAddress, nodeID string, validator sdk.AccAddress) (bool, error) {
	return k.TeeValidations.Has(ctx, teeValidationKey(stage, subject, nodeID, validator))
}

func (k Keeper) GetTeeValidation(ctx context.Context, stage int64, subject sdk.AccAddress, nodeID string, validator sdk.AccAddress) (types.TeeValidation, bool, error) {
	v, err := k.TeeValidations.Get(ctx, teeValidationKey(stage, subject, nodeID, validator))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.TeeValidation{}, false, nil
		}
		return types.TeeValidation{}, false, err
	}
	return v, true, nil
}

type TeeValidationSubject struct {
	Subject sdk.AccAddress
	NodeID  string
	Votes   []types.TeeValidation
}

func (k Keeper) TallyTeeValidationsForStage(ctx context.Context, stage int64) ([]TeeValidationSubject, error) {
	rng := collections.NewPrefixedQuadRange[int64, sdk.AccAddress, string, sdk.AccAddress](stage)
	var out []TeeValidationSubject
	err := k.TeeValidations.Walk(ctx, rng, func(key collections.Quad[int64, sdk.AccAddress, string, sdk.AccAddress], v types.TeeValidation) (bool, error) {
		subject, node := key.K2(), key.K3()
		if n := len(out); n > 0 && out[n-1].Subject.Equals(subject) && out[n-1].NodeID == node {
			out[n-1].Votes = append(out[n-1].Votes, v)
		} else {
			out = append(out, TeeValidationSubject{Subject: subject, NodeID: node, Votes: []types.TeeValidation{v}})
		}
		return false, nil
	})
	return out, err
}

func (k Keeper) PruneTeeValidationsForStage(ctx context.Context, stage int64) (int, error) {
	rng := collections.NewPrefixedQuadRange[int64, sdk.AccAddress, string, sdk.AccAddress](stage)
	var keys []collections.Quad[int64, sdk.AccAddress, string, sdk.AccAddress]
	if err := k.TeeValidations.Walk(ctx, rng, func(key collections.Quad[int64, sdk.AccAddress, string, sdk.AccAddress], _ types.TeeValidation) (bool, error) {
		keys = append(keys, key)
		return false, nil
	}); err != nil {
		return 0, err
	}
	for _, key := range keys {
		if err := k.TeeValidations.Remove(ctx, key); err != nil {
			return 0, err
		}
	}
	return len(keys), nil
}
