package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) GetUnitOfComputePriceProposal(goCtx context.Context, req *types.QueryGetUnitOfComputePriceProposalRequest) (*types.QueryGetUnitOfComputePriceProposalResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	proposal, _ := k.GettUnitOfComputePriceProposal(ctx, req.Participant)

	params := k.GetParams(ctx)

	return &types.QueryGetUnitOfComputePriceProposalResponse{
		Proposal: proposal,
		// TODO: Default should be int64
		Default: uint64(params.EpochParams.DefaultUnitOfComputePrice),
	}, nil
}
