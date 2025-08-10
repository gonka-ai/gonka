package types

import (
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgRequestBridgeWithdrawal{}

func NewMsgRequestBridgeWithdrawal(creator, wrappedContractAddress, amount, destinationAddress string) *MsgRequestBridgeWithdrawal {
	return &MsgRequestBridgeWithdrawal{
		Creator:                creator,
		WrappedContractAddress: wrappedContractAddress,
		Amount:                 amount,
		DestinationAddress:     destinationAddress,
	}
}

func (msg *MsgRequestBridgeWithdrawal) ValidateBasic() error {
	// Validate creator address
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}

	// Validate wrapped contract address
	_, err = sdk.AccAddressFromBech32(msg.WrappedContractAddress)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid wrapped contract address (%s)", err)
	}

	// Validate amount
	amount, ok := math.NewIntFromString(msg.Amount)
	if !ok || amount.LTE(math.ZeroInt()) {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "invalid amount (%s)", msg.Amount)
	}

	// Validate destination address (basic Ethereum address format)
	if len(msg.DestinationAddress) != 42 || msg.DestinationAddress[:2] != "0x" {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid Ethereum destination address (%s)", msg.DestinationAddress)
	}

	return nil
}
