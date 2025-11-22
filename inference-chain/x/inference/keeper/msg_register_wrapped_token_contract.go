package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// RegisterWrappedTokenContract sets the code id used for new wrapped-token instantiations.
func (k msgServer) RegisterWrappedTokenContract(goCtx context.Context, req *types.MsgRegisterWrappedTokenContract) (*types.MsgRegisterWrappedTokenContractResponse, error) {
	if k.GetAuthority() != req.Authority {
		return nil, errorsmod.Wrapf(types.ErrInvalidSigner, "invalid authority; expected %s, got %s", k.GetAuthority(), req.Authority)
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	k.SetWrappedTokenCodeID(ctx, req.CodeId)
	return &types.MsgRegisterWrappedTokenContractResponse{}, nil
}
