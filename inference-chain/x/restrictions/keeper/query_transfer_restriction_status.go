package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/productscience/inference/x/restrictions/types"
)

// TransferRestrictionStatus queries the current transfer restriction status
func (k Keeper) TransferRestrictionStatus(goCtx context.Context, req *types.QueryTransferRestrictionStatusRequest) (*types.QueryTransferRestrictionStatusResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	params := k.GetParams(ctx)

	currentHeight := uint64(ctx.BlockHeight())
	restrictionEndBlock := params.RestrictionEndBlock

	// Determine if restrictions are active
	isActive := currentHeight < restrictionEndBlock

	// Calculate remaining blocks
	var remainingBlocks uint64
	if isActive {
		remainingBlocks = restrictionEndBlock - currentHeight
	} else {
		remainingBlocks = 0
	}

	return &types.QueryTransferRestrictionStatusResponse{
		IsActive:            isActive,
		RestrictionEndBlock: restrictionEndBlock,
		CurrentBlockHeight:  currentHeight,
		RemainingBlocks:     remainingBlocks,
	}, nil
}
