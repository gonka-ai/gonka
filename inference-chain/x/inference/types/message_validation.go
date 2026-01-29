package types

import (
	"strings"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	shopspring "github.com/shopspring/decimal"
)

var _ sdk.Msg = &MsgValidation{}

func NewMsgValidation(creator string, id string, inferenceId string, responsePayload string, responseHash string, value *Decimal) *MsgValidation {
	return &MsgValidation{
		Creator:         creator,
		Id:              id,
		InferenceId:     inferenceId,
		ResponsePayload: responsePayload,
		ResponseHash:    responseHash,
		ValueDecimal:    value,
	}
}

func (msg *MsgValidation) ValidateBasic() error {
	// creator address
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// minimally required fields per handler usage
	if strings.TrimSpace(msg.InferenceId) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "inference_id is required")
	}
	// optional fields: if provided, they must not be only whitespace
	if msg.Id != "" && strings.TrimSpace(msg.Id) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "id cannot be only whitespace")
	}
	if msg.ResponsePayload != "" && strings.TrimSpace(msg.ResponsePayload) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "response_payload cannot be only whitespace")
	}
	if msg.ResponseHash != "" && strings.TrimSpace(msg.ResponseHash) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "response_hash cannot be only whitespace")
	}
	if msg.ValueDecimal != nil {
		decimalValue := msg.ValueDecimal.ToDecimal()
		// value in [0,1]
		if decimalValue.IsNegative() || decimalValue.GreaterThan(shopspring.NewFromInt(1)) {
			return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "value must be in [0,1]")
		}
	} else {
		if msg.Value < 0 || msg.Value > 1 {
			return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "value must be in [0,1]")
		}
	}
	return nil
}
