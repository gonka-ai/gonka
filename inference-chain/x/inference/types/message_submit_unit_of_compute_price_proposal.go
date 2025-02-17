package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitUnitOfComputePriceProposal{}

func NewMsgSubmitUnitOfComputePriceProposal(creator string, price uint64) *MsgSubmitUnitOfComputePriceProposal {
	return &MsgSubmitUnitOfComputePriceProposal{
		Creator: creator,
		Price:   price,
	}
}

func (msg *MsgSubmitUnitOfComputePriceProposal) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	return nil
}
