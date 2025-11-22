package keeper

import (
	"context"

	"github.com/cosmos/cosmos-sdk/types/query"
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

	models, pageRes, err := query.CollectionPaginate(
		ctx,
		k.Models,
		req.Pagination,
		func(_ string, value types.Model) (types.Model, error) {
			return value, nil
		},
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	k.LogInfo("Retrieved models", types.Inferences, "len(models)", len(models), "has_next_page", pageRes != nil && len(pageRes.NextKey) > 0)

	return &types.QueryModelsAllResponse{
		Model:      models,
		Pagination: pageRes,
	}, nil
}
