package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitNewParticipant{}

func NewMsgSubmitNewParticipant(creator string, url string, models []string) *MsgSubmitNewParticipant {
	return &MsgSubmitNewParticipant{
		Creator: creator,
		Url:     url,
		Models:  models,
	}
}

func (msg *MsgSubmitNewParticipant) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	return nil
}
