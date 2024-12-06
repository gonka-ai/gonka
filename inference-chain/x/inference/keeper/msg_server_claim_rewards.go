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

	totalCoins := settleAmount.RewardCoins + settleAmount.RefundCoins + settleAmount.WorkCoins
	err := k.PayParticipantFromEscrow(ctx, msg.Creator, totalCoins)
	if err != nil {
		return nil, err
	}
	k.RemoveSettleAmount(ctx, msg.Creator)

	return &types.MsgClaimRewardsResponse{
		Amount: totalCoins,
		Result: "Rewards claimed",
	}, nil
}
