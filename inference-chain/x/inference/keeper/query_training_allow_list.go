package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) TrainingAllowList(goCtx context.Context, req *types.QueryTrainingAllowListRequest) (*types.QueryTrainingAllowListResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	// Collect all addresses from the allow list
	var addrs []string
	switch req.Role {
	case 0:
		if err := k.TrainingExecAllowListSet.Walk(ctx, nil, func(a sdk.AccAddress) (bool, error) {
			addrs = append(addrs, a.String())
			return false, nil
		}); err != nil {
			return nil, err
		}
	case 1:
		if err := k.TrainingStartAllowListSet.Walk(ctx, nil, func(a sdk.AccAddress) (bool, error) {
			addrs = append(addrs, a.String())
			return false, nil
		}); err != nil {
			return nil, err
		}

	}
	return &types.QueryTrainingAllowListResponse{Addresses: addrs}, nil
}
