package types

import (
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// ValidateBasic performs basic validation of MsgTransferOwnership
func (msg *MsgTransferOwnership) ValidateBasic() error {
	// Validate genesis address
	if _, err := sdk.AccAddressFromBech32(msg.GenesisAddress); err != nil {
		return sdkerrors.Wrapf(ErrInvalidTransferRequest, "invalid genesis address %s: %v", msg.GenesisAddress, err)
	}

	// Validate recipient address
	if _, err := sdk.AccAddressFromBech32(msg.RecipientAddress); err != nil {
		return sdkerrors.Wrapf(ErrInvalidTransferRequest, "invalid recipient address %s: %v", msg.RecipientAddress, err)
	}

	// Prevent self-transfer
	if msg.GenesisAddress == msg.RecipientAddress {
		return sdkerrors.Wrapf(ErrInvalidTransferRequest, "cannot transfer to the same address %s", msg.GenesisAddress)
	}

	return nil
}

// GetSigners returns the signers of the MsgTransferOwnership
func (msg *MsgTransferOwnership) GetSigners() []sdk.AccAddress {
	addr, err := sdk.AccAddressFromBech32(msg.GenesisAddress)
	if err != nil {
		panic(err)
	}
	return []sdk.AccAddress{addr}
}
