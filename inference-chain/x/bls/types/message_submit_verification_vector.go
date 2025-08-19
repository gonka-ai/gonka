package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitVerificationVector{}

func (m *MsgSubmitVerificationVector) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(m.Creator); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidAddress, "invalid creator address")
	}
	if m.EpochId == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "epoch_id must be > 0")
	}
	if len(m.DealerValidity) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "dealer_validity must be non-empty")
	}
	return nil
}
