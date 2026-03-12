package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) ApproveBridgeTokenForTrading(goCtx context.Context, msg *types.MsgApproveBridgeTokenForTrading) (*types.MsgApproveBridgeTokenForTradingResponse, error) {
	if err := k.CheckPermission(goCtx, msg, GovernancePermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Check if token is already approved for trading
	if k.HasBridgeTradeApprovedToken(ctx, msg.ChainId, msg.ContractAddress) {
		k.LogWarn("Approve bridge token for trading: Token already approved",
			types.Messages,
			"chainId", msg.ChainId,
			"contractAddress", msg.ContractAddress)
		return &types.MsgApproveBridgeTokenForTradingResponse{}, nil
	}

	// Create the approved token record
	approvedToken := types.BridgeTokenReference{
		ChainId:         msg.ChainId,
		ContractAddress: msg.ContractAddress,
	}

	// Store the approved token
	if err := k.SetBridgeTradeApprovedToken(ctx, approvedToken); err != nil {
		return nil, err
	}

	k.LogInfo("Approve bridge token for trading: Token approved successfully",
		types.Messages,
		"chainId", msg.ChainId,
		"contractAddress", msg.ContractAddress)

	return &types.MsgApproveBridgeTokenForTradingResponse{}, nil
}
