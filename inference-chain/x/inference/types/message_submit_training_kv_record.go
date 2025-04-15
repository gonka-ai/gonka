package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitTrainingKvRecord{}

func NewMsgSubmitTrainingKvRecord(creator string, taskId uint64, participant string, key string, value string) *MsgSubmitTrainingKvRecord {
	return &MsgSubmitTrainingKvRecord{
		Creator:     creator,
		TaskId:      taskId,
		Participant: participant,
		Key:         key,
		Value:       value,
	}
}

func (msg *MsgSubmitTrainingKvRecord) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	return nil
}
