package keeper

import (
	"context"
	"fmt"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RevokeTeeAttestation(goCtx context.Context, msg *types.MsgRevokeTeeAttestation) (*types.MsgRevokeTeeAttestationResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ParticipantPermission); err != nil {
		return nil, err
	}
	if len(msg.NodeIds) == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "node_ids must not be empty")
	}

	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}
	maxEntries := int(types.DefaultMaxEntriesPerTeeAttestationMessage)
	if params.TeeAttestationParams != nil && params.TeeAttestationParams.MaxEntriesPerMessage > 0 {
		maxEntries = int(params.TeeAttestationParams.MaxEntriesPerMessage)
	}
	if len(msg.NodeIds) > maxEntries {
		return nil, sdkerrors.Wrap(types.ErrIllegalState,
			fmt.Sprintf("too many revoke entries: got %d, max %d", len(msg.NodeIds), maxEntries))
	}

	creatorAddr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, sdkerrors.Wrap(types.ErrInvalidAddress, fmt.Sprintf("invalid creator address: %v", err))
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	seen := make(map[string]struct{}, len(msg.NodeIds))
	removed := 0
	for _, nodeID := range msg.NodeIds {
		if nodeID == "" {
			return nil, sdkerrors.Wrap(types.ErrTeeAttestationNodeIdEmpty, "node_ids contains an empty string")
		}
		if _, dup := seen[nodeID]; dup {
			return nil, sdkerrors.Wrap(types.ErrDuplicateNodeId,
				fmt.Sprintf("node_id %q appears more than once in message", nodeID))
		}
		seen[nodeID] = struct{}{}

		_, existed, err := k.GetTeeAttestation(ctx, creatorAddr, nodeID)
		if err != nil {
			return nil, err
		}
		if err := k.RemoveTeeAttestation(ctx, creatorAddr, nodeID); err != nil {
			return nil, err
		}
		if existed {
			removed++
		}

		ctx.EventManager().EmitEvent(sdk.NewEvent(
			EventTypeTeeAttestationRevoked,
			sdk.NewAttribute(AttributeKeyTeeParticipant, msg.Creator),
			sdk.NewAttribute(AttributeKeyTeeNodeID, nodeID),
			sdk.NewAttribute("existed", fmt.Sprint(existed)),
		))
		k.LogInfo("[TeeAttestation] Revoked",
			types.PoC,
			"participant", msg.Creator,
			"node_id", nodeID,
			"existed", existed,
		)
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		EventTypeTeeAttestationRevoked,
		sdk.NewAttribute(AttributeKeyTeeParticipant, msg.Creator),
		sdk.NewAttribute(AttributeKeyTeeRevocationCount, fmt.Sprint(removed)),
	))
	return &types.MsgRevokeTeeAttestationResponse{}, nil
}
