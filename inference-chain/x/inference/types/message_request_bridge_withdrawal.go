package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgRequestBridgeWithdrawal{}

func NewMsgRequestBridgeWithdrawal(creator, userAddress, amount, destinationAddress string) *MsgRequestBridgeWithdrawal {
	return &MsgRequestBridgeWithdrawal{
		Creator:            creator,
		UserAddress:        userAddress,
		Amount:             amount,
		DestinationAddress: destinationAddress,
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

	// Validate amount is not empty
	if len(msg.Amount) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "amount cannot be empty")
	}

	// Validate destination address is not empty (Ethereum address format not validated here)
	if len(msg.DestinationAddress) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "destination address cannot be empty")
	}

	return nil
}

func (msg *MsgRequestBridgeWithdrawal) GetSigners() []sdk.AccAddress {
	creatorAddr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		panic(err)
	}
	return []sdk.AccAddress{creatorAddr}
}
