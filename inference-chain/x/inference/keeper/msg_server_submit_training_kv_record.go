package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitTrainingKvRecord(goCtx context.Context, msg *types.MsgSubmitTrainingKvRecord) (*types.MsgSubmitTrainingKvRecordResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// TODO: Handling the message
	_ = ctx

	return &types.MsgSubmitTrainingKvRecordResponse{}, nil
}
