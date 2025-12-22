package types

import (
	"strings"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/x/inference/utils"
)

var _ sdk.Msg = &MsgFinishInference{}

func NewMsgFinishInference(creator string, inferenceId string, responseHash string, responsePayload string, promptTokenCount uint64, completionTokenCount uint64, executedBy string) *MsgFinishInference {
	return &MsgFinishInference{
		Creator:              creator,
		InferenceId:          inferenceId,
		ResponseHash:         responseHash,
		ResponsePayload:      responsePayload,
		PromptTokenCount:     promptTokenCount,
		CompletionTokenCount: completionTokenCount,
		ExecutedBy:           executedBy,
	}
}

func (msg *MsgFinishInference) ValidateBasic() error {
	// creator is required signer
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// required addresses
	if _, err := sdk.AccAddressFromBech32(strings.TrimSpace(msg.ExecutedBy)); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid executed_by address (%s)", err)
	}
	if _, err := sdk.AccAddressFromBech32(strings.TrimSpace(msg.TransferredBy)); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid transferred_by address (%s)", err)
	}
	if _, err := sdk.AccAddressFromBech32(strings.TrimSpace(msg.RequestedBy)); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid requested_by address (%s)", err)
	}
	// all required fields non-empty
	if strings.TrimSpace(msg.ResponseHash) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "response_hash is required")
	}
	if strings.TrimSpace(msg.PromptHash) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "prompt_hash is required")
	}
	if strings.TrimSpace(msg.OriginalPromptHash) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "original_prompt_hash is required")
	}
	if strings.TrimSpace(msg.Model) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "model is required")
	}
	// request_timestamp must be > 0
	if msg.RequestTimestamp <= 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "request_timestamp must be > 0")
	}
	// signatures: required and must be base64 r||s (64 bytes)
	if err := utils.ValidateBase64RSig64("transfer_signature", strings.TrimSpace(msg.TransferSignature)); err != nil {
		return err
	}
	if err := utils.ValidateBase64RSig64("executor_signature", strings.TrimSpace(msg.ExecutorSignature)); err != nil {
		return err
	}
	// inference_id must also be a valid r||s signature per spec
	if err := utils.ValidateBase64RSig64("inference_id", strings.TrimSpace(msg.InferenceId)); err != nil {
		return err
	}
	return nil
}
