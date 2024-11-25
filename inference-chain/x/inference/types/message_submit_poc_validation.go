package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitPocValidation{}

func NewMsgSubmitPocValidation(creator string, participantAddress string, pocStageStartBlockHeight int64, nonces []int64, dist []float64, receivedDist []float64, rTarget float64, fraudThreshold float64, nInvalid int64, probabilityHonest float64, fraudDetected bool) *MsgSubmitPocValidation {
	return &MsgSubmitPocValidation{
		Creator:                  creator,
		ParticipantAddress:       participantAddress,
		PocStageStartBlockHeight: pocStageStartBlockHeight,
		Nonces:                   nonces,
		Dist:                     dist,
		ReceivedDist:             receivedDist,
		RTarget:                  rTarget,
		FraudThreshold:           fraudThreshold,
		NInvalid:                 nInvalid,
		ProbabilityHonest:        probabilityHonest,
		FraudDetected:            fraudDetected,
	}
}

func (msg *MsgSubmitPocValidation) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	return nil
}
