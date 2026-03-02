package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgBatchStartInference{}

func (msg *MsgBatchStartInference) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	if len(msg.Starts) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "starts must not be empty")
	}
	for i, start := range msg.Starts {
		if start == nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "start at index %d is nil", i)
		}
		if err := start.ValidateBasic(); err != nil {
			return errorsmod.Wrapf(err, "invalid start at index %d", i)
		}
	}
	return nil
}
