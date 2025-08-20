package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"strings"
)

var _ sdk.Msg = &MsgSetBarrier{}

func NewMsgSetBarrier(creator string, req *SetBarrierRequest) *MsgSetBarrier {
	return &MsgSetBarrier{Creator: creator, Req: req}
}

func (msg *MsgSetBarrier) ValidateBasic() error {
	// signer
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// req present
	if msg.Req == nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "req is required")
	}
	// req fields
	if strings.TrimSpace(msg.Req.BarrierId) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "req.barrier_id is required")
	}
	if strings.TrimSpace(msg.Req.NodeId) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "req.node_id is required")
	}
	if msg.Req.RunId == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "req.run_id must be > 0")
	}
	return nil
}
