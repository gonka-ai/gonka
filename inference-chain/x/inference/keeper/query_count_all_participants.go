package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) CountParticipants(ctx context.Context, _ *types.QueryCountAllParticipantsRequest) (*types.QueryCountAllParticipantsResponse, error) {
	total := k.CountAllParticipantsWithNotZeroBalance(ctx)
	return &types.QueryCountAllParticipantsResponse{Total: total}, nil
}
