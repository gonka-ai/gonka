package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) TrainingKvRecord(goCtx context.Context, req *types.QueryTrainingKvRecordRequest) (*types.QueryTrainingKvRecordResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	record, found := k.GetTrainingKVRecord(ctx, req.TaskId, req.Key)
	if !found {
		return nil, status.Error(codes.NotFound, "record not found")
	}

	return &types.QueryTrainingKvRecordResponse{
		Record: record,
	}, nil
}
