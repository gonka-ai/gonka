package types

import (
	"strings"

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
	// creator address (required). Uppercase bech32 decodes to the same account
	// and is rejected so registration cannot store a second spelling.
	if _, err := CanonicalAccAddress(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// url optional; if provided, must be a valid http/https URL with SSRF protection
	if strings.TrimSpace(msg.Url) != "" {
		if err := utils.ValidateURLWithSSRFProtection("url", msg.Url); err != nil {
			return err
		}
	}
	// validator_key optional; if provided, must be valid ED25519 (32 bytes base64)
	if msg.ValidatorKey != "" && strings.TrimSpace(msg.ValidatorKey) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidPubKey, "invalid validator key: empty or whitespace")
	}
	if strings.TrimSpace(msg.ValidatorKey) != "" {
		if _, err := utils.SafeCreateED25519ValidatorKey(msg.ValidatorKey); err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidPubKey, "invalid validator key: %s", err)
		}
	}
	// worker_key is optional; if provided (non-empty after trim) it must be valid ED25519 compressed
	if msg.WorkerKey != "" && strings.TrimSpace(msg.WorkerKey) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidPubKey, "invalid worker key: empty or whitespace")
	}
	if strings.TrimSpace(msg.WorkerKey) != "" {
		if _, err := utils.SafeCreateED25519ValidatorKey(msg.WorkerKey); err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidPubKey, "invalid worker key: %s", err)
		}
	}
	return nil
}
