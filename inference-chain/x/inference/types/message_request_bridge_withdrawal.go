package types

import (
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgRequestBridgeWithdrawal{}

func NewMsgRequestBridgeWithdrawal(creator, userAddress, amount, destinationAddress, destinationBridgeAddress string) *MsgRequestBridgeWithdrawal {
	return &MsgRequestBridgeWithdrawal{
		Creator:                  creator,
		UserAddress:              userAddress,
		Amount:                   amount,
		DestinationAddress:       destinationAddress,
		DestinationBridgeAddress: destinationBridgeAddress,
	}
}

func (msg *MsgRequestBridgeWithdrawal) ValidateBasic() error {
	// Validate creator address (contract signer)
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}

	// Validate user address
	_, err = sdk.AccAddressFromBech32(msg.UserAddress)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid user address (%s)", err)
	}

	// Validate amount is a positive integer
	amountInt, ok := math.NewIntFromString(msg.Amount)
	if !ok {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "amount must be a valid integer")
	}
	if !amountInt.IsPositive() {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "amount must be positive")
	}

	// Validate destination address is not empty (Ethereum address format not validated here)
	if len(msg.DestinationAddress) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "destination address cannot be empty")
	}

	// Validate destination bridge address is not empty
	if len(msg.DestinationBridgeAddress) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "destination bridge address cannot be empty")
	}

	// Basic validation for Ethereum address format since bridge is Ethereum specific currently
	if !isValidEthereumAddress(msg.DestinationBridgeAddress) {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "destination bridge address must be a valid Ethereum address")
	}

	return nil
}
