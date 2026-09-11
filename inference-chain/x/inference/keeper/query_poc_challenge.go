package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/keeper/pocchallenge"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) ChallengeGenerationState(ctx context.Context, req *types.QueryChallengeGenerationStateRequest) (*types.QueryChallengeGenerationStateResponse, error) {
	if req == nil || req.Target == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()
	return pocchallenge.GenerationState(ctx, k.PoCChallenge, req.Target, height, pocchallenge.SliceBlocks(params))
}

func (k Keeper) OpenPoCChallenges(ctx context.Context, req *types.QueryOpenPoCChallengesRequest) (*types.QueryOpenPoCChallengesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	return pocchallenge.OpenChallenges(ctx, k.PoCChallenge, pocchallenge.SliceBlocks(params))
}
