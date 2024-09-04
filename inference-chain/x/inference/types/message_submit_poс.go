package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitPoC{}

func NewMsgSubmitPoC(creator string, blockHeight int64, nonce []string) *MsgSubmitPoC {
	return &MsgSubmitPoC{
		Creator:     creator,
		BlockHeight: blockHeight,
		Nonce:       nonce,
	}
}

func (msg *MsgSubmitPoC) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	return nil
}
