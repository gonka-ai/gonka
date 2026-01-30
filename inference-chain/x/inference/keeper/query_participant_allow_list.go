package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) ParticipantAllowList(goCtx context.Context, req *types.QueryParticipantAllowListRequest) (*types.QueryParticipantAllowListResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	var addrs []string
	if err := k.ParticipantAllowListSet.Walk(ctx, nil, func(a sdk.AccAddress) (bool, error) {
		addrs = append(addrs, a.String())
		return false, nil
	}); err != nil {
		return nil, err
	}

	return &types.QueryParticipantAllowListResponse{Addresses: addrs}, nil
}

