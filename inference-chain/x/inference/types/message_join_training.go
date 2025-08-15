package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"strings"
)

var _ sdk.Msg = &MsgJoinTraining{}

func NewMsgJoinTraining(creator string) *MsgJoinTraining {
	return &MsgJoinTraining{
		Creator: creator,
	}
}

func (msg *MsgJoinTraining) ValidateBasic() error {
	// signer
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// req present
	if msg.Req == nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "req is required")
	}
	// req fields: node_id non-empty, run_id > 0, outer_step >= 0
	if strings.TrimSpace(msg.Req.NodeId) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "req.node_id is required")
	}
	if msg.Req.RunId == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "req.run_id must be > 0")
	}
	if msg.Req.OuterStep < 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "req.outer_step cannot be negative")
	}
	return nil
}
