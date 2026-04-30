package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) GovernanceCancelBridgeOperation(goCtx context.Context, msg *types.MsgGovernanceCancelBridgeOperation) (*types.MsgGovernanceCancelBridgeOperationResponse, error) {
	if err := k.CheckPermission(goCtx, msg, GovernancePermission); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	if err := k.cancelBridgeOperation(ctx, msg.RequestId, msg.Authority, bridgeCancelOptions{
		isGovernance:            true,
		reason:                  msg.Reason,
		overrideRecipient:       msg.OverrideRecipient,
		overrideWrappedContract: msg.OverrideWrappedContract,
	}); err != nil {
		return nil, err
	}

	return &types.MsgGovernanceCancelBridgeOperationResponse{}, nil
}
