package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitNewUnfundedParticipant{}

func NewMsgSubmitNewUnfundedParticipant(creator string, address string, url string, models []string, pubKey string, validatorKey string) *MsgSubmitNewUnfundedParticipant {
	return &MsgSubmitNewUnfundedParticipant{
		Creator:      creator,
		Address:      address,
		Url:          url,
		PubKey:       pubKey,
		ValidatorKey: validatorKey,
	}
}

func (msg *MsgSubmitNewUnfundedParticipant) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	return nil
}
