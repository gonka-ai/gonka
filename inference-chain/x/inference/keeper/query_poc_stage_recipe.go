package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) PocStageRecipe(ctx context.Context, req *types.QueryPocStageRecipeRequest) (*types.QueryPocStageRecipeResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	recipe, found, err := k.GetPocStageRecipe(ctx, req.StageHeight)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if !found {
		return &types.QueryPocStageRecipeResponse{Found: false}, nil
	}
	return &types.QueryPocStageRecipeResponse{Recipe: &recipe, Found: true}, nil
}
