package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"strings"
)

var _ sdk.Msg = &MsgCreateDummyTrainingTask{}

func NewMsgCreateDummyTrainingTask(creator string, task *TrainingTask) *MsgCreateDummyTrainingTask {
	return &MsgCreateDummyTrainingTask{Creator: creator, Task: task}
}

func (msg *MsgCreateDummyTrainingTask) ValidateBasic() error {
	// signer
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// embedded task must be present
	if msg.Task == nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "task is required")
	}
	// Minimal structural checks: hardware_resources types non-empty if present; assignees fields if present
	for i, hr := range msg.Task.HardwareResources {
		if strings.TrimSpace(hr.Type) == "" {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "task.hardware_resources[%d].type is required", i)
		}
		// hr.Count is uint32; no negativity possible; no upper bound here
	}
	for i, a := range msg.Task.Assignees {
		if strings.TrimSpace(a.Participant) == "" {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "task.assignees[%d].participant is required", i)
		}
		if _, err := sdk.AccAddressFromBech32(a.Participant); err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "task.assignees[%d].participant invalid (%s)", i, err)
		}
		for j, nid := range a.NodeIds {
			if strings.TrimSpace(nid) == "" {
				return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "task.assignees[%d].node_ids[%d] must be non-empty", i, j)
			}
		}
	}
	return nil
}
