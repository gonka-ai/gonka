package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitPartialSignature{}

func (m *MsgSubmitPartialSignature) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(m.Creator); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidAddress, "invalid creator address")
	}
	if len(m.SlotIndices) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "slot_indices must be non-empty")
	}
	return nil
}
