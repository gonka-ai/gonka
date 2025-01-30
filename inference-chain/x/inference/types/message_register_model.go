package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgRegisterModel{}

func NewMsgRegisterModel(creator string, id string, unitsOfComputePerToken uint64) *MsgRegisterModel {
	return &MsgRegisterModel{
		Creator:                creator,
		Id:                     id,
		UnitsOfComputePerToken: unitsOfComputePerToken,
	}
}

func (msg *MsgRegisterModel) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	return nil
}
