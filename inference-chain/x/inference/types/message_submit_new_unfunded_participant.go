package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/x/inference/utils"
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

	// Validate Address field if provided
	if msg.Address != "" {
		_, err := sdk.AccAddressFromBech32(msg.Address)
		if err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid address (%s)", err)
		}
	}

	// Validate PubKey (SECP256K1 account key) if provided
	if msg.PubKey != "" {
		_, err := utils.SafeCreateSECP256K1AccountKey(msg.PubKey)
		if err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidPubKey, "invalid pub key: %s", err)
		}
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
