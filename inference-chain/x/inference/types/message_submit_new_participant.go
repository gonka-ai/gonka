package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/x/inference/utils"
	"strings"
)

var _ sdk.Msg = &MsgSubmitNewParticipant{}

func NewMsgSubmitNewParticipant(creator string, url string, models []string) *MsgSubmitNewParticipant {
	return &MsgSubmitNewParticipant{
		Creator: creator,
		Url:     url,
	}
}

func (msg *MsgSubmitNewParticipant) ValidateBasic() error {
	// creator address (required)
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// url required and must be valid
	if strings.TrimSpace(msg.Url) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "url is required")
	}
	if err := utils.ValidateURL("url", msg.Url); err != nil {
		return err
	}
	// validator_key required and must be valid ED25519 (32 bytes base64)
	if strings.TrimSpace(msg.ValidatorKey) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidPubKey, "invalid validator key: empty or whitespace")
	}
	if _, err := utils.SafeCreateED25519ValidatorKey(msg.ValidatorKey); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidPubKey, "invalid validator key: %s", err)
	}
	// worker_key is optional; if provided (non-empty after trim) it must be valid SECP256K1 compressed
	if strings.TrimSpace(msg.WorkerKey) != "" {
		if _, err := utils.SafeCreateSECP256K1AccountKey(msg.WorkerKey); err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidPubKey, "invalid worker key: %s", err)
		}
	}
	return nil
}
