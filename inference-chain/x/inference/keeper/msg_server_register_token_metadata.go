package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RegisterTokenMetadata(goCtx context.Context, msg *types.MsgRegisterTokenMetadata) (*types.MsgRegisterTokenMetadataResponse, error) {
	if err := k.CheckPermission(goCtx, msg, GovernancePermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Create TokenMetadata struct from the message
	metadata := TokenMetadata{
		Name:      msg.Name,
		Symbol:    msg.Symbol,
		Decimals:  uint8(msg.Decimals),
		Overwrite: msg.Overwrite,
	}

	// Set the token metadata and update the wrapped token contract if it exists
	err := k.SetTokenMetadataAndUpdateContract(ctx, msg.ChainId, msg.ContractAddress, metadata)
	if err != nil {
		return nil, err
	}

	return &types.MsgRegisterTokenMetadataResponse{}, nil
}
