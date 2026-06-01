package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgCancelBridgeOperation{}

func NewMsgCancelBridgeOperation(creator string, requestID string) *MsgCancelBridgeOperation {
	return &MsgCancelBridgeOperation{
		Creator:   creator,
		RequestId: requestID,
	}
}

func (msg *MsgCancelBridgeOperation) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}

	if len(msg.RequestId) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "request id cannot be empty")
	}

	return nil
}
