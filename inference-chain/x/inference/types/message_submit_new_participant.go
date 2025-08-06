package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/x/inference/utils"
)

var _ sdk.Msg = &MsgSubmitNewParticipant{}

func NewMsgSubmitNewParticipant(creator string, url string, models []string) *MsgSubmitNewParticipant {
	return &MsgSubmitNewParticipant{
		Creator: creator,
		Url:     url,
	}
}

func (msg *MsgSubmitNewParticipant) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}

	// Validate ValidatorKey (ED25519)
	if msg.ValidatorKey != "" {
		_, err := utils.SafeCreateED25519ValidatorKey(msg.ValidatorKey)
		if err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidPubKey, "invalid validator key: %s", err)
		}
	}

	return nil
}
