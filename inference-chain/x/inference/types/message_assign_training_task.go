package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"strings"
)

var _ sdk.Msg = &MsgAssignTrainingTask{}

func NewMsgAssignTrainingTask(creator string) *MsgAssignTrainingTask {
	return &MsgAssignTrainingTask{
		Creator: creator,
	}
}

func (msg *MsgAssignTrainingTask) ValidateBasic() error {
	// signer
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// task_id must be > 0
	if msg.TaskId == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "task_id must be > 0")
	}
	// assignees must be non-empty
	if len(msg.Assignees) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "assignees must be non-empty")
	}
	// each assignee: participant bech32, node_ids non-empty and trimmed
	for i, a := range msg.Assignees {
		if _, err := sdk.AccAddressFromBech32(strings.TrimSpace(a.Participant)); err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "assignees[%d].participant invalid (%s)", i, err)
		}
		if len(a.NodeIds) == 0 {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "assignees[%d].node_ids must be non-empty", i)
		}
		for j, nid := range a.NodeIds {
			if strings.TrimSpace(nid) == "" {
				return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "assignees[%d].node_ids[%d] must be non-empty", i, j)
			}
		}
	}
	return nil
}
