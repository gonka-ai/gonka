package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitPocBatch{}

func NewMsgSubmitPocBatch(creator string, pocStageStartBlockHeight int64, nonces []int64, dist []float64) *MsgSubmitPocBatch {
	return &MsgSubmitPocBatch{
		Creator:                  creator,
		PocStageStartBlockHeight: pocStageStartBlockHeight,
		Nonces:                   nonces,
		Dist:                     dist,
	}
}

func (msg *MsgSubmitPocBatch) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	return nil
}
