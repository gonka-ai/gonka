package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var (
	_ sdk.Msg = &MsgSetPoCDelegation{}
	_ sdk.Msg = &MsgRefusePoCDelegation{}
	_ sdk.Msg = &MsgDeclarePoCIntent{}
)

func (msg *MsgSetPoCDelegation) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Sender); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid sender address (%s)", err)
	}
	if msg.ModelId == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "model_id required")
	}
	if msg.DelegateTo != "" {
		if _, err := sdk.AccAddressFromBech32(msg.DelegateTo); err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid delegate_to address (%s)", err)
		}
		if msg.Sender == msg.DelegateTo {
			return errorsmod.Wrap(ErrSelfDelegation, "sender cannot delegate to self")
		}
	}
	return nil
}

func (msg *MsgRefusePoCDelegation) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Sender); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid sender address (%s)", err)
	}
	if msg.ModelId == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "model_id required")
	}
	return nil
}

func (msg *MsgDeclarePoCIntent) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Sender); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid sender address (%s)", err)
	}
	if msg.ModelId == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "model_id required")
	}
	return nil
}
