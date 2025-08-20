package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgClaimTrainingTaskForAssignment{}

func NewMsgClaimTrainingTaskForAssignment(creator string) *MsgClaimTrainingTaskForAssignment {
	return &MsgClaimTrainingTaskForAssignment{
		Creator: creator,
	}
}

func (msg *MsgClaimTrainingTaskForAssignment) ValidateBasic() error {
	// signer
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// task_id must be > 0
	if msg.TaskId == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "task_id must be > 0")
	}
	return nil
}
