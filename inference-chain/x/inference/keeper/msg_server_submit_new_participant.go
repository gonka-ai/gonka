package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitNewParticipant(goCtx context.Context, msg *types.MsgSubmitNewParticipant) (*types.MsgSubmitNewParticipantResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	newParticipant := types.Participant{
		Index:             msg.Creator,
		Address:           msg.Creator,
		Reputation:        1,
		Weight:            1,
		JoinTime:          ctx.BlockTime().UnixMilli(),
		JoinHeight:        ctx.BlockHeight(),
		LastInferenceTime: 0,
		InferenceUrl:      msg.Url,
		Models:            msg.Models,
		Status:            types.ParticipantStatus_ACTIVE,
	}

	k.SetParticipant(ctx, newParticipant)

	return &types.MsgSubmitNewParticipantResponse{}, nil
}
