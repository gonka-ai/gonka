package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgGovernanceCancelBridgeOperation{}

func NewMsgGovernanceCancelBridgeOperation(
	authority string,
	requestID string,
	overrideRecipient string,
	overrideWrappedContract string,
	reason string,
) *MsgGovernanceCancelBridgeOperation {
	return &MsgGovernanceCancelBridgeOperation{
		Authority:               authority,
		RequestId:               requestID,
		OverrideRecipient:       overrideRecipient,
		OverrideWrappedContract: overrideWrappedContract,
		Reason:                  reason,
	}
}

func (msg *MsgGovernanceCancelBridgeOperation) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Authority); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid authority address (%s)", err)
	}
	if len(msg.RequestId) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "request id cannot be empty")
	}
	if msg.OverrideRecipient != "" {
		if _, err := sdk.AccAddressFromBech32(msg.OverrideRecipient); err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid override recipient address (%s)", err)
		}
	}
	if msg.OverrideWrappedContract != "" {
		if _, err := sdk.AccAddressFromBech32(msg.OverrideWrappedContract); err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid override wrapped contract address (%s)", err)
		}
	}
	return nil
}
