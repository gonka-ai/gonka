package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"math"
	"strings"
)

var _ sdk.Msg = &MsgTrainingHeartbeat{}

func NewMsgTrainingHeartbeat(creator string, req *HeartbeatRequest) *MsgTrainingHeartbeat {
	return &MsgTrainingHeartbeat{Creator: creator, Req: req}
}

func (msg *MsgTrainingHeartbeat) ValidateBasic() error {
	// signer
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// req present
	if msg.Req == nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "req is required")
	}
	// req fields
	if strings.TrimSpace(msg.Req.NodeId) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "req.node_id is required")
	}
	if msg.Req.RunId == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "req.run_id must be > 0")
	}
	if msg.Req.Epoch < 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "req.epoch cannot be negative")
	}
	// timestamp must be finite (not NaN/Inf). It can be zero or positive.
	if math.IsNaN(msg.Req.Timestamp) || math.IsInf(msg.Req.Timestamp, 0) {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "req.timestamp must be finite")
	}
	return nil
}
