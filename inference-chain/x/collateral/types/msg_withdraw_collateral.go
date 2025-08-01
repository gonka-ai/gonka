package types

import (
	"cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgWithdrawCollateral{}

// NewMsgWithdrawCollateral creates a new MsgWithdrawCollateral instance
func NewMsgWithdrawCollateral(participant string, amount sdk.Coin) *MsgWithdrawCollateral {
	return &MsgWithdrawCollateral{
		Participant: participant,
		Amount:      amount,
	}
}

// ValidateBasic does a sanity check on the provided data
func (msg *MsgWithdrawCollateral) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Participant)
	if err != nil {
		return errors.Wrapf(sdkerrors.ErrInvalidAddress, "invalid participant address (%s)", err)
	}

	if !msg.Amount.IsValid() {
		return errors.Wrap(sdkerrors.ErrInvalidCoins, "invalid withdrawal amount")
	}

	if msg.Amount.IsZero() {
		return errors.Wrap(sdkerrors.ErrInvalidCoins, "withdrawal amount cannot be zero")
	}

	return nil
}
