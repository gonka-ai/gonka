package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// RegisterWrappedTokenContract sets the code id used for new wrapped-token instantiations.
func (k msgServer) RegisterWrappedTokenContract(goCtx context.Context, req *types.MsgRegisterWrappedTokenContract) (*types.MsgRegisterWrappedTokenContractResponse, error) {
	if err := k.CheckPermission(goCtx, req, GovernancePermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	if err := k.SetWrappedTokenCodeID(ctx, req.CodeId); err != nil {
		return nil, err
	}
	return &types.MsgRegisterWrappedTokenContractResponse{}, nil
}
