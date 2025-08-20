package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgRequestThresholdSignature{}

func (m *MsgRequestThresholdSignature) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(m.Creator); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidAddress, "invalid creator address")
	}
	if m.CurrentEpochId == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "current_epoch_id must be > 0")
	}
	if len(m.Data) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "data must be non-empty")
	}
	return nil
}
