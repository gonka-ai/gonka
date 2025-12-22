package types

import (
	"strings"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/x/inference/utils"
)

var _ sdk.Msg = &MsgStartInference{}

func NewMsgStartInference(creator string, inferenceId string, promptHash string, promptPayload string, requestedBy string) *MsgStartInference {
	return &MsgStartInference{
		Creator:       creator,
		InferenceId:   inferenceId,
		PromptHash:    promptHash,
		PromptPayload: promptPayload,
		RequestedBy:   requestedBy,
	}
}

func (msg *MsgStartInference) ValidateBasic() error {
	// creator is required signer
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// required bech32 addresses
	if _, err := sdk.AccAddressFromBech32(strings.TrimSpace(msg.RequestedBy)); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid requested_by address (%s)", err)
	}
	if _, err := sdk.AccAddressFromBech32(strings.TrimSpace(msg.AssignedTo)); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid assigned_to address (%s)", err)
	}
	// required strings
	if err := utils.ValidateBase64RSig64("inference_id", strings.TrimSpace(msg.InferenceId)); err != nil {
		return err
	}
	if strings.TrimSpace(msg.Model) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "model is required")
	}
	if strings.TrimSpace(msg.PromptHash) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "prompt_hash is required")
	}
	if strings.TrimSpace(msg.OriginalPromptHash) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "original_prompt_hash is required")
	}
	// request_timestamp must be > 0
	if msg.RequestTimestamp <= 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "request_timestamp must be > 0")
	}
	// signatures: transfer_signature required & valid; inference_id already validated above
	if err := utils.ValidateBase64RSig64("transfer_signature", strings.TrimSpace(msg.TransferSignature)); err != nil {
		return err
	}
	return nil
}
