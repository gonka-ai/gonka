package keeper

import (
	"context"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitNewParticipant(goCtx context.Context, msg *types.MsgSubmitNewParticipant) (*types.MsgSubmitNewParticipantResponse, error) {
	if err := k.CheckPermission(goCtx, msg, OpenRegistrationPermission); err != nil {
		return nil, err
	}
	// ValidateBasic is not consensus admission. Reject a non-canonical creator
	// here, before any read or write, so a proposer that skipped CheckTx
	// cannot register or update an uppercase spelling.
	addr, err := types.CanonicalAccAddress(msg.GetCreator())
	if err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	canonical := addr.String()

	// Check if participant already exists. If it does, restrict updates to only
	// ValidatorKey, WorkerKey, and Url as per requirements.
	if existing, found := k.GetParticipant(ctx, canonical); found {
		// Preserve all existing fields and update only the allowed ones
		// Whitespace follows the empty-string rule: it must not erase a live address.
		if trimmed := strings.TrimSpace(msg.Url); trimmed != "" {
			existing.InferenceUrl = trimmed
		}
		if msg.ValidatorKey != "" {
			existing.ValidatorKey = msg.ValidatorKey
		}
		if msg.WorkerKey != "" {
			existing.WorkerPublicKey = msg.WorkerKey
		}
		if err := k.SetParticipant(ctx, existing); err != nil {
			return nil, err
		}
		return &types.MsgSubmitNewParticipantResponse{}, nil
	}

	// If participant does not exist yet, create a new one
	newParticipant := createNewParticipant(ctx, msg, addr)
	if err := k.SetParticipant(ctx, newParticipant); err != nil {
		return nil, err
	}
	return &types.MsgSubmitNewParticipantResponse{}, nil
}

func createNewParticipant(ctx sdk.Context, msg *types.MsgSubmitNewParticipant, addr sdk.AccAddress) types.Participant {
	canonical := addr.String()
	newParticipant := types.Participant{
		Index:             canonical,
		Address:           canonical,
		Weight:            -1,
		JoinTime:          ctx.BlockTime().UnixMilli(),
		JoinHeight:        ctx.BlockHeight(),
		LastInferenceTime: 0,
		InferenceUrl:      strings.TrimSpace(msg.GetUrl()),
		Status:            types.ParticipantStatus_ACTIVE,
		ValidatorKey:      msg.GetValidatorKey(),
		WorkerPublicKey:   msg.GetWorkerKey(),
		CurrentEpochStats: types.NewCurrentEpochStats(),
	}

	return newParticipant
}
