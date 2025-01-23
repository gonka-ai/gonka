package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitUnitOfComputePriceProposal(goCtx context.Context, msg *types.MsgSubmitUnitOfComputePriceProposal) (*types.MsgSubmitUnitOfComputePriceProposalResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// TODO: Handling the message
	_ = ctx

	return &types.MsgSubmitUnitOfComputePriceProposalResponse{}, nil
}
