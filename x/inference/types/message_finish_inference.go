package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
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
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	return nil
}
