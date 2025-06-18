package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) CountParticipants(ctx context.Context, req *types.QueryCountAllParticipantsRequest) (*types.QueryCountAllParticipantsResponse, error) {
	total := k.CountAllParticipants(ctx)
	return &types.QueryCountAllParticipantsResponse{Total: total}, nil
}
