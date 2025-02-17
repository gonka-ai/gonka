package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) ModelsAll(goCtx context.Context, req *types.QueryModelsAllRequest) (*types.QueryModelsAllResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	models, err := k.GetAllModels(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	modelsValues, err := PointersToValues(models)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	k.LogInfo("Retrieved models", "len(models)", len(modelsValues), "models", modelsValues)

	return &types.QueryModelsAllResponse{
		Model: modelsValues,
	}, nil
}
