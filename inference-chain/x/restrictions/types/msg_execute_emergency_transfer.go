package types

import (
	"strconv"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgExecuteEmergencyTransfer{}

// ValidateBasic does a sanity check on the provided data.
func (m *MsgExecuteEmergencyTransfer) ValidateBasic() error {
	// Validate exemption ID
	if m.ExemptionId == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "exemption ID cannot be empty")
	}

	// Validate from address
	if _, err := sdk.AccAddressFromBech32(m.FromAddress); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid from address: %s", err)
	}

	// Validate to address
	if _, err := sdk.AccAddressFromBech32(m.ToAddress); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid to address: %s", err)
	}

	// Validate amount
	if m.Amount == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "amount cannot be empty")
	}

	// Parse amount to ensure it's a valid number
	if _, err := strconv.ParseUint(m.Amount, 10, 64); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "invalid amount format: %s", err)
	}

	// Validate denomination
	if m.Denom == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "denomination cannot be empty")
	}

	// Validate denomination format (basic validation)
	if err := sdk.ValidateDenom(m.Denom); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "invalid denomination: %s", err)
	}

	return nil
}

// GetSigners returns the addresses that must sign the transaction.
func (m *MsgExecuteEmergencyTransfer) GetSigners() []sdk.AccAddress {
	from, err := sdk.AccAddressFromBech32(m.FromAddress)
	if err != nil {
		panic(err)
	}
	return []sdk.AccAddress{from}
}
