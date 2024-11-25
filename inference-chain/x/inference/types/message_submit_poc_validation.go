package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitPocValidation{}

func NewMsgSubmitPocValidation(creator string, participantAddress string, pocStageStartBlockHeight int32, nonces []int32, dist []int32, receivedDist []int32, rTarget int32, fraudThreshold int32, nInvalid int32, probabilityHonest int32, fraudDetected bool) *MsgSubmitPocValidation {
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
