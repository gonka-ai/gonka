package types

import (
	"strings"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgValidation{}

func NewMsgValidation(creator string, id string, inferenceId string, responsePayload string, responseHash string, value float64) *MsgValidation {
	return &MsgValidation{
		Creator:         creator,
		Id:              id,
		InferenceId:     inferenceId,
		ResponsePayload: responsePayload,
		ResponseHash:    responseHash,
		Value:           value,
	}
}

func (msg *MsgValidation) ValidateBasic() error {
	// creator address
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// all fields required
	if strings.TrimSpace(msg.Id) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "id is required")
	}
	if strings.TrimSpace(msg.InferenceId) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "inference_id is required")
	}
	if strings.TrimSpace(msg.ResponsePayload) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "response_payload is required")
	}
	if strings.TrimSpace(msg.ResponseHash) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "response_hash is required")
	}
	// value in [0,1]
	if msg.Value < 0 || msg.Value > 1 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "value must be in [0,1]")
	}
	return nil
}
