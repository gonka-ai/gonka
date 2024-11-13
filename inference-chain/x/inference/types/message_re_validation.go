package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgReValidation{}

func NewMsgReValidation(creator string, inferenceId string, responsePayload string, responseHash string, value string) *MsgReValidation {
	return &MsgReValidation{
		Creator:         creator,
		InferenceId:     inferenceId,
		ResponsePayload: responsePayload,
		ResponseHash:    responseHash,
		Value:           value,
	}
}

func (msg *MsgReValidation) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	return nil
}
