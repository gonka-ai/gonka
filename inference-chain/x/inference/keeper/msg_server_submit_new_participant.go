package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitNewParticipant(goCtx context.Context, msg *types.MsgSubmitNewParticipant) (*types.MsgSubmitNewParticipantResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Check if participant already exists. If it does, restrict updates to only
	// ValidatorKey, WorkerKey, and Url as per requirements.
	if existing, found := k.GetParticipant(ctx, msg.GetCreator()); found {
		// Preserve all existing fields and update only the allowed ones
		if msg.Url != "" {
			existing.InferenceUrl = msg.Url
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
	newParticipant := createNewParticipant(ctx, msg)
	if err := k.SetParticipant(ctx, newParticipant); err != nil {
		return nil, err
	}
	return &types.MsgSubmitNewParticipantResponse{}, nil
}

func createNewParticipant(ctx sdk.Context, msg *types.MsgSubmitNewParticipant) types.Participant {
	newParticipant := types.Participant{
		Index:             msg.GetCreator(),
		Address:           msg.GetCreator(),
		Weight:            -1,
		JoinTime:          ctx.BlockTime().UnixMilli(),
		JoinHeight:        ctx.BlockHeight(),
		LastInferenceTime: 0,
		InferenceUrl:      msg.GetUrl(),
		Status:            types.ParticipantStatus_ACTIVE,
		ValidatorKey:      msg.GetValidatorKey(),
		WorkerPublicKey:   msg.GetWorkerKey(),
		CurrentEpochStats: types.NewCurrentEpochStats(),
	}

	return newParticipant
}
