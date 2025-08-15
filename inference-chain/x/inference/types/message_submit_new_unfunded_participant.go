package types

import (
	"strings"

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
	// creator address (required)
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// address required and valid
	if strings.TrimSpace(msg.Address) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "address is required")
	}
	if _, err := sdk.AccAddressFromBech32(msg.Address); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid address (%s)", err)
	}
	// URL optional; if provided, must be valid format
	if strings.TrimSpace(msg.Url) != "" {
		if err := utils.ValidateURL("url", msg.Url); err != nil {
			return err
		}
	}
	// PubKey required: SECP256K1 compressed account key
	if strings.TrimSpace(msg.PubKey) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidPubKey, "invalid pub key: empty or whitespace")
	}
	if _, err := utils.SafeCreateSECP256K1AccountKey(msg.PubKey); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidPubKey, "invalid pub key: %s", err)
	}
	// ValidatorKey optional: ED25519
	if msg.ValidatorKey != "" && strings.TrimSpace(msg.ValidatorKey) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidPubKey, "invalid validator key: empty or whitespace")
	}
	if strings.TrimSpace(msg.ValidatorKey) != "" {
		if _, err := utils.SafeCreateED25519ValidatorKey(msg.ValidatorKey); err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidPubKey, "invalid validator key: %s", err)
		}
	}
	// WorkerKey is optional: if provided (non-empty after trim), must be SECP256K1 compressed
	if strings.TrimSpace(msg.WorkerKey) != "" {
		if _, err := utils.SafeCreateED25519ValidatorKey(msg.WorkerKey); err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidPubKey, "invalid worker key: %s", err)
		}
	}
	return nil
}
