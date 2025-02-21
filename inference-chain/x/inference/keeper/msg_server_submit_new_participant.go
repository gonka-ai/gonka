package keeper

import (
	"context"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitNewParticipant(goCtx context.Context, msg *types.MsgSubmitNewParticipant) (*types.MsgSubmitNewParticipantResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	newParticipant := createNewParticipant(ctx, msg)
	k.SetParticipant(ctx, newParticipant)

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
		Models:            msg.GetModels(),
		Status:            types.ParticipantStatus_ACTIVE,
		ValidatorKey:      msg.GetValidatorKey(),
		WorkerPublicKey:   msg.GetWorkerKey(),
	}

	return newParticipant
}
