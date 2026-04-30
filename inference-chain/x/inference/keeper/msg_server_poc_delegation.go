package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SetPoCDelegation(ctx context.Context, msg *types.MsgSetPoCDelegation) (*types.MsgSetPoCDelegationResponse, error) {
	if err := k.CheckPermission(ctx, msg, ParticipantPermission); err != nil {
		return nil, err
	}

	if _, found := k.GetGovernanceModel(ctx, msg.ModelId); !found {
		return nil, types.ErrInvalidModel
	}

	if msg.DelegateTo == "" {
		// Clear delegation
		_ = k.Keeper.DeletePoCDelegation(ctx, msg.ModelId, msg.Sender)
	} else {
		if _, found := k.GetParticipant(ctx, msg.DelegateTo); !found {
			return nil, types.ErrParticipantNotFound
		}
		if err := k.Keeper.SetPoCDelegation(ctx, types.PoCDelegation{
			ModelId:    msg.ModelId,
			Delegator:  msg.Sender,
			DelegateTo: msg.DelegateTo,
		}); err != nil {
			return nil, err
		}
	}

	// Last-write-wins: clear refusal and intent
	k.Keeper.ClearOtherDelegationState(ctx, msg.ModelId, msg.Sender, "delegation")

	return &types.MsgSetPoCDelegationResponse{}, nil
}

func (k msgServer) RefusePoCDelegation(ctx context.Context, msg *types.MsgRefusePoCDelegation) (*types.MsgRefusePoCDelegationResponse, error) {
	if err := k.CheckPermission(ctx, msg, ParticipantPermission); err != nil {
		return nil, err
	}

	if _, found := k.GetGovernanceModel(ctx, msg.ModelId); !found {
		return nil, types.ErrInvalidModel
	}

	if err := k.Keeper.SetPoCRefusal(ctx, msg.ModelId, msg.Sender); err != nil {
		return nil, err
	}

	// Last-write-wins: clear delegation and intent
	k.Keeper.ClearOtherDelegationState(ctx, msg.ModelId, msg.Sender, "refusal")

	return &types.MsgRefusePoCDelegationResponse{}, nil
}

func (k msgServer) DeclarePoCIntent(ctx context.Context, msg *types.MsgDeclarePoCIntent) (*types.MsgDeclarePoCIntentResponse, error) {
	if err := k.CheckPermission(ctx, msg, ParticipantPermission); err != nil {
		return nil, err
	}

	if _, found := k.GetGovernanceModel(ctx, msg.ModelId); !found {
		return nil, types.ErrInvalidModel
	}

	if err := k.Keeper.SetPoCDirectIntent(ctx, msg.ModelId, msg.Sender); err != nil {
		return nil, err
	}

	// Last-write-wins: clear delegation and refusal
	k.Keeper.ClearOtherDelegationState(ctx, msg.ModelId, msg.Sender, "intent")

	return &types.MsgDeclarePoCIntentResponse{}, nil
}
