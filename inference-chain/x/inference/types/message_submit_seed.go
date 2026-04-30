package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/x/inference/utils"
)

var _ sdk.Msg = &MsgSubmitSeed{}

func NewMsgSubmitSeed(creator string, seed int64, epochId uint64, signature string) *MsgSubmitSeed {
	return &MsgSubmitSeed{
		Creator:    creator,
		EpochIndex: epochId,
		Signature:  signature,
	}
}

func (msg *MsgSubmitSeed) ValidateBasic() error {
	// signer
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// epoch_index must be > 0
	if msg.EpochIndex <= 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "epoch_index must be > 0")
	}
	// signature required and must be hex-encoded 64 bytes (r||s)
	if err := utils.ValidateHexRSig64("signature", msg.Signature); err != nil {
		return err
	}
	return nil
}
