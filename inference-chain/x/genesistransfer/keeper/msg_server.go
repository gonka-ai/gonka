package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/genesistransfer/types"
)

type msgServer struct {
	Keeper
}

// NewMsgServerImpl returns an implementation of the MsgServer interface
// for the provided Keeper.
func NewMsgServerImpl(keeper Keeper) types.MsgServer {
	return &msgServer{Keeper: keeper}
}

var _ types.MsgServer = msgServer{}

// TransferOwnership handles the MsgTransferOwnership message
func (k msgServer) TransferOwnership(goCtx context.Context, msg *types.MsgTransferOwnership) (*types.MsgTransferOwnershipResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Validate message basic
	if err := msg.ValidateBasic(); err != nil {
		return nil, errorsmod.Wrapf(err, "invalid message")
	}

	// Parse addresses
	genesisAddr, err := sdk.AccAddressFromBech32(msg.GenesisAddress)
	if err != nil {
		return nil, errorsmod.Wrapf(err, "invalid genesis address: %s", msg.GenesisAddress)
	}

	recipientAddr, err := sdk.AccAddressFromBech32(msg.RecipientAddress)
	if err != nil {
		return nil, errorsmod.Wrapf(err, "invalid recipient address: %s", msg.RecipientAddress)
	}

	// Execute the complete ownership transfer
	if err := k.ExecuteOwnershipTransfer(ctx, genesisAddr, recipientAddr); err != nil {
		return nil, errorsmod.Wrapf(err, "ownership transfer failed")
	}

	// Log successful execution
	k.Logger().Info(
		"ownership transfer message processed successfully",
		"genesis_address", msg.GenesisAddress,
		"recipient_address", msg.RecipientAddress,
	)

	return &types.MsgTransferOwnershipResponse{}, nil
}
