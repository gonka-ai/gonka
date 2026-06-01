package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// ApproveIbcTokenForTrading approves an IBC token for trading in the liquidity pool
func (k msgServer) ApproveIbcTokenForTrading(goCtx context.Context, msg *types.MsgApproveIbcTokenForTrading) (*types.MsgApproveIbcTokenForTradingResponse, error) {
	if err := k.CheckPermission(goCtx, msg, GovernancePermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Check if token is already approved for trading
	// Since IBC tokens reuse the bridge token storage map, we can use the same check.
	if k.HasBridgeTradeApprovedToken(ctx, msg.ChainId, msg.IbcDenom) {
		k.LogWarn("Approve IBC token for trading: Token already approved",
			types.Messages,
			"chainId", msg.ChainId,
			"ibcDenom", msg.IbcDenom)
		return &types.MsgApproveIbcTokenForTradingResponse{}, nil
	}

	// Create the approved token record
	approvedToken := types.BridgeTokenReference{
		ChainId:         msg.ChainId,
		ContractAddress: msg.IbcDenom,
	}

	// Store the approved token using IBC-specific logic (validates '/' and stores)
	if err := k.SetIBCTradeApprovedToken(ctx, approvedToken); err != nil {
		return nil, err
	}

	k.LogInfo("Approve IBC token for trading: Token approved successfully",
		types.Messages,
		"chainId", msg.ChainId,
		"ibcDenom", msg.IbcDenom)

	return &types.MsgApproveIbcTokenForTradingResponse{}, nil
}
