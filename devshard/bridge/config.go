package bridge

import (
	"devshard/logging"
	"devshard/types"
)

// SessionConfigAtBind builds the session config from per-escrow chain fields (lane A).
// Zero ValidationRate (older chain/dapi that omit the field) falls through to
// types.DefaultValidationRate via SessionConfigFromEscrow.
func SessionConfigAtBind(groupSize int, escrow *EscrowInfo) types.SessionConfig {
	var escrowID string
	var escrowRate uint32
	var cfg types.SessionConfig
	if escrow == nil {
		cfg = types.SessionConfigFromEscrow(groupSize, types.EscrowSessionFields{})
	} else {
		escrowID = escrow.EscrowID
		escrowRate = escrow.ValidationRate
		cfg = types.SessionConfigFromEscrow(groupSize, types.EscrowSessionFields{
			TokenPrice:                escrow.TokenPrice,
			CreateDevshardFee:         escrow.CreateDevshardFee,
			FeePerNonce:               escrow.FeePerNonce,
			InferenceSealGraceNonces:  escrow.InferenceSealGraceNonces,
			InferenceSealGraceSeconds: escrow.InferenceSealGraceSeconds,
			AutoSealEveryNNonces:      escrow.AutoSealEveryNNonces,
			ValidationRate:            escrow.ValidationRate,
		})
	}
	logging.Debug("validation_rate_bound",
		"subsystem", "validation",
		"escrow_id", escrowID,
		"escrow_validation_rate", escrowRate,
		"applied_validation_rate", cfg.ValidationRate,
		"default_validation_rate", types.DefaultValidationRate,
		"used_default", escrowRate == 0,
	)
	return cfg
}
