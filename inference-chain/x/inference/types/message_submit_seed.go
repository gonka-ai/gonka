package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitSeed{}

func NewMsgSubmitSeed(creator string, seed int64, blockHeight int64, signature string) *MsgSubmitSeed {
	return &MsgSubmitSeed{
		Creator:     creator,
		BlockHeight: blockHeight,
		Signature:   signature,
	}
}

func (msg *MsgSubmitSeed) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	return nil
}
