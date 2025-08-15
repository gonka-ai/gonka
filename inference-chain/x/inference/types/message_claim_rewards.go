package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgClaimRewards{}

func NewMsgClaimRewards(creator string, seed int64, pocStartHeight uint64) *MsgClaimRewards {
	return &MsgClaimRewards{Creator: creator, Seed: seed, PocStartHeight: pocStartHeight}
}

func (msg *MsgClaimRewards) ValidateBasic() error {
	// signer
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// poc_start_height must be > 0
	if msg.PocStartHeight == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "poc_start_height must be > 0")
	}
	// seed is allowed to be any int64; no additional stateless checks
	return nil
}
