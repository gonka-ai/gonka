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
	sender, err := CanonicalAccAddress(msg.Sender)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid sender address (%s)", err)
	}
	if msg.ModelId == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "model_id required")
	}
	if msg.DelegateTo != "" {
		delegate, err := CanonicalAccAddress(msg.DelegateTo)
		if err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid delegate_to address (%s)", err)
		}
		if sender.Equals(delegate) {
			return errorsmod.Wrap(ErrSelfDelegation, "sender cannot delegate to self")
		}
	}
	return nil
}

func (msg *MsgRefusePoCDelegation) ValidateBasic() error {
	if _, err := CanonicalAccAddress(msg.Sender); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid sender address (%s)", err)
	}
	if msg.ModelId == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "model_id required")
	}
	return nil
}

func (msg *MsgDeclarePoCIntent) ValidateBasic() error {
	if _, err := CanonicalAccAddress(msg.Sender); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid sender address (%s)", err)
	}
	if msg.ModelId == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "model_id required")
	}
	return nil
}
