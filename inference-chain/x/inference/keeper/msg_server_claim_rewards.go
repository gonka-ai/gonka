package keeper

import (
	"context"
	"github.com/productscience/inference/x/inference/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k msgServer) ClaimRewards(goCtx context.Context, msg *types.MsgClaimRewards) (*types.MsgClaimRewardsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	settleAmount, found := k.GetSettleAmount(ctx, msg.Creator)
	if !found {
		return &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "No rewards for this address",
		}, nil
	}
	if settleAmount.PocStartHeight != msg.PocStartHeight {
		return &types.MsgClaimRewardsResponse{
			Amount: 0,
			Result: "No rewards for this block height",
		}, nil
	}

	totalCoins := settleAmount.GetTotalCoins()
	k.LogInfo("Issuing rewards", "address", msg.Creator, "amount", totalCoins)
	err := k.PayParticipantFromEscrow(ctx, msg.Creator, totalCoins)
	if err != nil {
		k.LogError("Error paying participant", "error", err)
		// Big question: do we remove the settle amount? Probably not
		return nil, err
	}
	k.RemoveSettleAmount(ctx, msg.Creator)

	return &types.MsgClaimRewardsResponse{
		Amount: totalCoins,
		Result: "Rewards claimed",
	}, nil
}
