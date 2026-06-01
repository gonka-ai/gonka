package keeper

import (
	"context"
	"strings"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RegisterBridgeAddresses(goCtx context.Context, msg *types.MsgRegisterBridgeAddresses) (*types.MsgRegisterBridgeAddressesResponse, error) {
	if err := k.CheckPermission(goCtx, msg, GovernancePermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

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

		// If this address was previously (or fraudulently) registered as a CW20
		// wrapped token, remove both the forward mapping and the reverse index before
		// registering it as a bridge address. The orphaned CW20 contract remains on-chain
		// but becomes unreachable from the inference module — RequestBridgeWithdrawal will
		// reject it because getWrappedTokenMetadata will return found=false.
		if existingWrapped, found := k.GetWrappedTokenContract(ctx, chainId, address); found {
			k.LogWarn("Register bridge addresses: Removing stale wrapped token record for bridge address",
				types.Messages,
				"chainId", chainId,
				"bridgeAddress", address,
				"orphanedCW20", existingWrapped.WrappedContractAddress,
			)
			tokenKey := collections.Join(chainId, strings.ToLower(address))
			
			if err := k.WrappedTokenContractsMap.Remove(ctx, tokenKey); err != nil {
				return nil, err
			}
			if err := k.WrappedContractReverseIndex.Remove(ctx, strings.ToLower(existingWrapped.WrappedContractAddress)); err != nil {
				return nil, err
			}
			if err := k.WrappedTokenMetadataMap.Remove(ctx, tokenKey); err != nil {
				return nil, err
			}
			if err := k.LiquidityPoolApprovedTokensMap.Remove(ctx, tokenKey); err != nil {
				return nil, err
			}
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
