package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitGroupKeyValidationSignature{}

func (m *MsgSubmitGroupKeyValidationSignature) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(m.Creator); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidAddress, "invalid creator address")
	}
	if m.NewEpochId == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "new_epoch_id must be > 0")
	}
	if len(m.SlotIndices) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "slot_indices must be non-empty")
	}
	if len(m.SlotIndices) > MaxSlotIndices {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "slot_indices exceed maximum allowed")
	}
	return nil
}
