package bridge

import "devshard/types"

// SessionConfigAtBind builds the session config from per-escrow chain fields (lane A).
func SessionConfigAtBind(groupSize int, escrow *EscrowInfo) types.SessionConfig {
	if escrow == nil {
		return types.SessionConfigFromEscrow(groupSize, types.EscrowSessionFields{})
	}
	return types.SessionConfigFromEscrow(groupSize, types.EscrowSessionFields{
		TokenPrice:                escrow.TokenPrice,
		CreateDevshardFee:         escrow.CreateDevshardFee,
		FeePerNonce:               escrow.FeePerNonce,
		InferenceSealGraceNonces:  escrow.InferenceSealGraceNonces,
		InferenceSealGraceSeconds: escrow.InferenceSealGraceSeconds,
		AutoSealEveryNNonces:      escrow.AutoSealEveryNNonces,
		ValidationRate:            escrow.ValidationRate,
		VoteThresholdFactor:       escrow.VoteThresholdFactor,
	})
}
