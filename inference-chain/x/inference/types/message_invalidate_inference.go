package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgInvalidateInference{}

func NewMsgInvalidateInference(creator string, inferenceId string) *MsgInvalidateInference {
	return &MsgInvalidateInference{
		Creator:     creator,
		InferenceId: inferenceId,
	}
}

func (msg *MsgInvalidateInference) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	return nil
}
