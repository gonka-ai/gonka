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

	var recipe types.PocStageRecipe
	var found bool
	var err error
	switch lookup := req.Lookup.(type) {
	case *types.QueryPocStageRecipeRequest_StageHeight:
		recipe, found, err = k.GetPocStageRecipe(ctx, lookup.StageHeight)
	case *types.QueryPocStageRecipeRequest_EpochIndex:
		recipe, found, err = k.GetEpochPocRecipe(ctx, lookup.EpochIndex)
	default:
		return nil, status.Error(codes.InvalidArgument, "specify stage_height or epoch_index")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if !found {
		return &types.QueryPocStageRecipeResponse{Found: false}, nil
	}
	return &types.QueryPocStageRecipeResponse{Recipe: &recipe, Found: true}, nil
}
