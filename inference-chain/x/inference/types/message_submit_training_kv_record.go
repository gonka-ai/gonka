package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"strings"
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
	// signer
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// taskId > 0
	if msg.TaskId == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "taskId must be > 0")
	}
	// participant bech32
	if _, err := sdk.AccAddressFromBech32(strings.TrimSpace(msg.Participant)); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid participant address (%s)", err)
	}
	// key/value non-empty trimmed
	if strings.TrimSpace(msg.Key) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "key is required")
	}
	if strings.TrimSpace(msg.Value) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "value is required")
	}
	return nil
}
