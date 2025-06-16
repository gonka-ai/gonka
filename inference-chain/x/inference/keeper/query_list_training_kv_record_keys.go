package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) ListTrainingKvRecordKeys(goCtx context.Context, req *types.QueryListTrainingKvRecordKeysRequest) (*types.QueryListTrainingKvRecordKeysResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	response, err := k.ListTrainingKVRecords(ctx, req.TaskId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	keys := make([]string, len(response))
	for i, record := range response {
		keys[i] = record.Key
	}

	return &types.QueryListTrainingKvRecordKeysResponse{
		Keys: keys,
	}, nil
}
