package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitUnitOfComputePriceProposal(goCtx context.Context, msg *types.MsgSubmitUnitOfComputePriceProposal) (*types.MsgSubmitUnitOfComputePriceProposalResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	eg, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		return nil, err
	}

	k.SetUnitOfComputePriceProposal(ctx, &types.UnitOfComputePriceProposal{
		Price:                 msg.Price,
		Participant:           msg.Creator,
		ProposedAtBlockHeight: uint64(ctx.BlockHeight()),
		ProposedAtEpoch:       eg.GroupData.EpochGroupId,
	})

	return &types.MsgSubmitUnitOfComputePriceProposalResponse{}, nil
}
