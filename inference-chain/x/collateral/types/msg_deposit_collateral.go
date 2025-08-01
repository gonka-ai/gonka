package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgDepositCollateral{}

// ValidateBasic performs basic validation of the MsgDepositCollateral
func (msg *MsgDepositCollateral) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Participant)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid participant address: %s", err)
	}

	if !msg.Amount.IsValid() {
		return errorsmod.Wrap(sdkerrors.ErrInvalidCoins, "invalid collateral amount")
	}

	if !msg.Amount.IsPositive() {
		return errorsmod.Wrap(sdkerrors.ErrInvalidCoins, "collateral amount must be positive")
	}

	return nil
}
