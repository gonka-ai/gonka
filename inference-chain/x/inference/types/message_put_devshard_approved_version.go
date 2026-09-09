package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgPutDevshardApprovedVersion{}

func (msg *MsgPutDevshardApprovedVersion) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Authority)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid authority address (%s)", err)
	}
	if err := msg.Version.Validate(); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, err.Error())
	}
	return nil
}
