package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgBatchTransferWithVesting{}

func (m *MsgBatchTransferWithVesting) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(m.Sender); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid sender address: %s", err)
	}

	if len(m.Outputs) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "outputs cannot be empty")
	}

	for i, output := range m.Outputs {
		if _, err := sdk.AccAddressFromBech32(output.Recipient); err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid recipient address at index %d: %s", i, err)
		}

		if output.Amount.IsZero() {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidCoins, "amount cannot be zero for recipient at index %d", i)
		}

		if !output.Amount.IsValid() {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidCoins, "invalid coins for recipient at index %d", i)
		}

		if err := validateMinTransferAmounts(output.Amount); err != nil {
			return errorsmod.Wrapf(err, "recipient at index %d (%s)", i, output.Recipient)
		}
	}

	return nil
}
