package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RegisterBridgeAddresses(goCtx context.Context, msg *types.MsgRegisterBridgeAddresses) (*types.MsgRegisterBridgeAddressesResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Validate authority - only governance can register bridge addresses
	if msg.Authority != k.GetAuthority() {
		return nil, types.ErrInvalidSigner
	}

	// Use the chain name directly as chainId (e.g., "ethereum", "polygon")
	chainId := msg.ChainName

	// Register addresses with chainId
	for _, address := range msg.Addresses {
		// Check if address already exists for this chain
		if k.HasBridgeContractAddress(ctx, chainId, address) {
			k.LogWarn("Register bridge addresses: Address already registered",
				types.Messages,
				"chainId", chainId,
				"address", address)
			continue
		}

		bridgeAddr := types.BridgeContractAddress{
			Id:      k.generateBridgeAddressKey(ctx, chainId, address),
			ChainId: chainId,
			Address: address,
		}
		k.SetBridgeContractAddress(ctx, bridgeAddr)
	}

	k.LogInfo("Register bridge addresses: Proposal completed",
		types.Messages,
		"chainId", chainId,
	)

	return &types.MsgRegisterBridgeAddressesResponse{}, nil
}
