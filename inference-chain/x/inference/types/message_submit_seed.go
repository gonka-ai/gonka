package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/x/inference/utils"
)

var _ sdk.Msg = &MsgSubmitSeed{}

func NewMsgSubmitSeed(creator string, seed int64, blockHeight int64, signature string) *MsgSubmitSeed {
	return &MsgSubmitSeed{
		Creator:     creator,
		BlockHeight: blockHeight,
		Signature:   signature,
	}
}

func (msg *MsgSubmitSeed) ValidateBasic() error {
	// signer
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// block_height must be > 0
	if msg.BlockHeight <= 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "block_height must be > 0")
	}
	// signature required and must decode to 64 bytes (r||s)
	if err := utils.ValidateBase64RSig64("signature", msg.Signature); err != nil {
		return err
	}
	return nil
}
