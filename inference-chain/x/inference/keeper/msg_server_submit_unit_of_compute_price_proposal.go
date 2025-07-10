package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitUnitOfComputePriceProposal(goCtx context.Context, msg *types.MsgSubmitUnitOfComputePriceProposal) (*types.MsgSubmitUnitOfComputePriceProposalResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	blockHeight := ctx.BlockHeight()
	effectiveEpoch, found := k.GetEffectiveEpoch(ctx)
	if !found {
		k.LogError("SubmitUnitOfComputePriceProposal: No effective epoch found", types.Pricing)
		return nil, sdkerrors.Wrapf(types.ErrEffectiveEpochNotFound, "SubmitUnitOfComputePriceProposal: No effective epoch found. blockHeight: %d", blockHeight)
	}

	k.SetUnitOfComputePriceProposal(ctx, &types.UnitOfComputePriceProposal{
		Price:                 msg.Price,
		Participant:           msg.Creator,
		ProposedAtBlockHeight: uint64(blockHeight),
		ProposedAtEpoch:       effectiveEpoch.Index,
	})

	return &types.MsgSubmitUnitOfComputePriceProposalResponse{}, nil
}
