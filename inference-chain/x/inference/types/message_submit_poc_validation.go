package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgSubmitPocValidation{}

func NewMsgSubmitPocValidation(
	creator string,
	participantAddr string,
	pocStageStartBlockHeight int64,
	nonces []int64,
	dist []float64,
	receivedDist []float64,
	rTarget float64,
	fraudThreshold float64,
	nInvalid int64,
	probHonest float64,
	fraudDetected bool,
) *MsgSubmitPocValidation {
	return &MsgSubmitPocValidation{
		Creator:                  creator,
		ParticipantAddress:       participantAddr,
		PocStageStartBlockHeight: pocStageStartBlockHeight,
		Nonces:                   nonces,
		Dist:                     dist,
		ReceivedDist:             receivedDist,
		RTarget:                  rTarget,
		FraudThreshold:           fraudThreshold,
		NInvalid:                 nInvalid,
		ProbabilityHonest:        probHonest,
		FraudDetected:            fraudDetected,
	}
}

func (msg *MsgSubmitPocValidation) ValidateBasic() error {
	// bech32 signers
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	if _, err := sdk.AccAddressFromBech32(msg.ParticipantAddress); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid participant_address (%s)", err)
	}
	// height > 0
	if msg.PocStageStartBlockHeight <= 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "poc_stage_start_block_height must be > 0")
	}
	// For now, all these lists will be empty! See post_generate_batches_handler.go
	//if len(msg.Nonces) == 0 {
	//	return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "nonces must be non-empty")
	//}
	//if len(msg.Dist) == 0 {
	//	return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "dist must be non-empty")
	//}
	//if len(msg.ReceivedDist) == 0 {
	//	return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "received_dist must be non-empty")
	//}
	//// lengths must match: we require same length across all 3 lists
	//if len(msg.Nonces) != len(msg.Dist) || len(msg.Dist) != len(msg.ReceivedDist) {
	//	return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "nonces, dist, and received_dist must have the same length: %d, %d, %d", len(msg.Nonces), len(msg.Dist), len(msg.ReceivedDist))
	//}
	//// per-element validation
	//for i, n := range msg.Nonces {
	//	if n < 0 {
	//		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "nonces[%d] must be >= 0", i)
	//	}
	//}
	//for i, d := range msg.Dist {
	//	if math.IsNaN(d) || math.IsInf(d, 0) {
	//		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "dist[%d] must be finite", i)
	//	}
	//	if d < 0 || d > 1 {
	//		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "dist[%d] must be in [0,1]", i)
	//	}
	//}
	//for i, d := range msg.ReceivedDist {
	//	if math.IsNaN(d) || math.IsInf(d, 0) {
	//		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "received_dist[%d] must be finite", i)
	//	}
	//	if d < 0 || d > 1 {
	//		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "received_dist[%d] must be in [0,1]", i)
	//	}
	//}
	// thresholds and counts
	//if math.IsNaN(msg.RTarget) || math.IsInf(msg.RTarget, 0) || msg.RTarget < 0 || msg.RTarget > 1 {
	//	return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "r_target must be in [0,1] and finite")
	//}
	//if math.IsNaN(msg.FraudThreshold) || math.IsInf(msg.FraudThreshold, 0) || msg.FraudThreshold < 0 || msg.FraudThreshold > 1 {
	//	return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "fraud_threshold must be in [0,1] and finite")
	//}
	//if msg.NInvalid < 0 {
	//	return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "n_invalid must be >= 0")
	//}
	//if math.IsNaN(msg.ProbabilityHonest) || math.IsInf(msg.ProbabilityHonest, 0) || msg.ProbabilityHonest < 0 || msg.ProbabilityHonest > 1 {
	//	return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "probability_honest must be in [0,1] and finite")
	//}
	return nil
}
