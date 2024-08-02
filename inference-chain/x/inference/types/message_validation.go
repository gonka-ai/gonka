package types

import (
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
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	return nil
}
