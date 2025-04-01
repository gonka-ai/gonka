package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgCreatePartialUpgrade{}

func NewMsgCreatePartialUpgrade(creator string, height uint64, nodeVersion string, apiBinariesJson string) *MsgCreatePartialUpgrade {
	return &MsgCreatePartialUpgrade{
		Authority:       creator,
		Height:          height,
		NodeVersion:     nodeVersion,
		ApiBinariesJson: apiBinariesJson,
	}
}

func (msg *MsgCreatePartialUpgrade) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Authority)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	return nil
}
