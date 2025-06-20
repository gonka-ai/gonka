package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgCreateDummyTrainingTask{}

func NewMsgCreateDummyTrainingTask(creator string) *MsgCreateDummyTrainingTask {
	return &MsgCreateDummyTrainingTask{
		Creator: creator,
	}
}

func (msg *MsgCreateDummyTrainingTask) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	return nil
}
