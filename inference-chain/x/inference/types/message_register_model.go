package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgRegisterModel{}

func NewMsgRegisterModel(authority string, proposedBy string, id string, unitsOfComputePerToken uint64) *MsgRegisterModel {
	return &MsgRegisterModel{
		Authority:              authority,
		ProposedBy:             proposedBy,
		Id:                     id,
		UnitsOfComputePerToken: unitsOfComputePerToken,
	}
}

func (msg *MsgRegisterModel) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Authority)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid authrority address (%s)", err)
	}
	_, err = sdk.AccAddressFromBech32(msg.ProposedBy)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid proposedBy address (%s)", err)
	}
	return nil
}
