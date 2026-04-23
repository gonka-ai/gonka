package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) UpdateParams(goCtx context.Context, req *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	if err := k.CheckPermission(goCtx, req, GovernancePermission); err != nil {
		return nil, err
	}

	if err := req.Params.Validate(); err != nil {
		return nil, errorsmod.Wrap(err, "invalid params")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	if err := k.SetParams(ctx, req.Params); err != nil {
		return nil, err
	}
	err := k.PrecomputeSPRTValues(ctx)
	if err != nil {
		k.LogError("Failed to precompute SPRT values", types.Validation, "error", err)
		return nil, err
	}

	return &types.MsgUpdateParamsResponse{}, nil
}
