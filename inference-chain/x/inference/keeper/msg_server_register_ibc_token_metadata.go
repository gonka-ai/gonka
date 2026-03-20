package keeper

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RegisterIbcTokenMetadata(goCtx context.Context, msg *types.MsgRegisterIbcTokenMetadata) (*types.MsgRegisterIbcTokenMetadataResponse, error) {
	if err := k.CheckPermission(goCtx, msg, GovernancePermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Validate that this is strictly an IBC denom to protect native chain denoms (e.g. ugnk)
	if !strings.HasPrefix(msg.IbcDenom, "ibc/") {
		return nil, fmt.Errorf("invalid denom: must start with 'ibc/' to protect native token metadata")
	}

	// Protect against overwriting existing Bank metadata unless explicitly requested
	_, existingBankMetaFound := k.BankView.GetDenomMetaData(ctx, msg.IbcDenom)
	if existingBankMetaFound && !msg.Overwrite {
		return nil, fmt.Errorf("bank metadata for denom %s already exists, set Overwrite=true to overwrite", msg.IbcDenom)
	}

	// Create BridgeTokenMetadata struct from the message
	metadata := types.BridgeTokenMetadata{
		ChainId:         msg.ChainId,
		ContractAddress: msg.IbcDenom, // For IBC tokens, the "contract address" is the denom
		Name:            msg.Name,
		Symbol:          msg.Symbol,
		Decimals:        uint32(msg.Decimals),
	}

	// Set the IBC token metadata in our custom store
	err := k.SetIBCTokenMetadata(ctx, msg.ChainId, msg.IbcDenom, metadata)
	if err != nil {
		return nil, err
	}

	// Also update x/bank denom metadata so standard Cosmos tools and explorers
	// can query correct decimals/symbol via bank/v1beta1/denoms_metadata
	bankMetadata := banktypes.Metadata{
		Description: fmt.Sprintf("IBC token from %s, registered via governance", msg.ChainId),
		DenomUnits: []*banktypes.DenomUnit{
			{
				Denom:    msg.IbcDenom,
				Exponent: 0,
			},
			{
				Denom:    msg.Symbol,
				Exponent: uint32(msg.Decimals),
			},
		},
		Base:    msg.IbcDenom,
		Display: msg.Symbol,
		Name:    msg.Name,
		Symbol:  msg.Symbol,
	}
	k.BankView.SetDenomMetaData(ctx, bankMetadata)

	return &types.MsgRegisterIbcTokenMetadataResponse{}, nil
}
