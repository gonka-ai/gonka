package types

const (
	defaultInferenceSealGraceMultiplier = 1 // for tests
	minInferenceSealGraceNonces         = 20
	// DefaultInferenceSealGraceSeconds is the wall-clock grace before sealing
	// stale-finished inferences. Must match inference-chain
	// DefaultDevshardInferenceSealGraceSeconds (3600 = 1 hour).
	DefaultInferenceSealGraceSeconds = 3600
	// DefaultAutoSealEveryNNonces is how often auto-seal runs during Active phase.
	// Must match inference-chain DefaultDevshardAutoSealEveryNNonces.
	DefaultAutoSealEveryNNonces uint32 = 150
	// DefaultValidationRate matches inference-chain DefaultDevshardValidationRate.
	DefaultValidationRate uint32 = 5000
)

// DefaultInferenceSealGraceNonces returns the canonical seal grace for a session group.
// Phase 1 uses a nonce gate of 10 * groupSize with a floor of 20 so small
// groups still leave enough room for post-terminal traffic before sealing.
func DefaultInferenceSealGraceNonces(groupSize int) uint32 {
	grace := groupSize * defaultInferenceSealGraceMultiplier
	if grace < minInferenceSealGraceNonces {
		grace = minInferenceSealGraceNonces
	}
	return uint32(grace)
}

// NormalizeSessionConfig applies derived defaults that must be fixed once a
// session is created. Zero values that have protocol meaning (such as timeout=0)
// are preserved; only fields with explicit "unset means use canonical default"
// semantics are filled here.
func NormalizeSessionConfig(cfg SessionConfig, groupSize int) SessionConfig {
	if cfg.InferenceSealGraceNonces == 0 {
		cfg.InferenceSealGraceNonces = DefaultInferenceSealGraceNonces(groupSize)
	}
	if cfg.InferenceSealGraceSeconds == 0 {
		cfg.InferenceSealGraceSeconds = DefaultInferenceSealGraceSeconds
	}
	if cfg.AutoSealEveryNNonces == 0 {
		cfg.AutoSealEveryNNonces = DefaultAutoSealEveryNNonces
	}
	return cfg
}

// DefaultSessionConfig returns the canonical session config that both user and
// host must use. A single source of truth prevents state root divergence caused
// by config mismatches (e.g. different ValidationRate values).
func DefaultSessionConfig(groupSize int) SessionConfig {
	return NormalizeSessionConfig(SessionConfig{
		RefusalTimeout:    60,
		ExecutionTimeout:  1200,
		TokenPrice:        1,
		CreateDevshardFee: 10_000,
		FeePerNonce:       1_000,
		VoteThreshold:     uint32(groupSize) / 2,
		ValidationRate:    DefaultValidationRate,
	}, groupSize)
}

// EscrowSessionFields collects per-escrow parameters frozen onto DevshardEscrow
// at create. Every field is "zero means use the compiled default" so callers can
// populate only what the chain returned.
type EscrowSessionFields struct {
	TokenPrice                  uint64
	CreateDevshardFee           uint64
	FeePerNonce                 uint64
	InferenceSealGraceNonces    uint32
	InferenceSealGraceSeconds   uint32
	AutoSealEveryNNonces        uint32
	ValidationRate              uint32
	VoteThresholdFactor         uint32 // percent; 0 == legacy groupSize/2
}

// ComputeVoteThreshold derives the slot-majority vote threshold from group
// size and governance vote_threshold_factor (percent). factor == 0 uses the
// legacy groupSize/2 fallback.
func ComputeVoteThreshold(groupSize int, factor uint32) uint32 {
	if factor == 0 {
		return uint32(groupSize) / 2
	}
	return uint32(groupSize) * factor / 100
}

// SessionConfigFromEscrow builds a SessionConfig by starting from the
// compiled DefaultSessionConfig and overlaying any non-zero per-escrow values.
//
// Zero fields fall through to defaults so legacy escrows (no snapshot)
// keep today's behavior.
func SessionConfigFromEscrow(groupSize int, fields EscrowSessionFields) SessionConfig {
	cfg := DefaultSessionConfig(groupSize)
	if fields.TokenPrice > 0 {
		cfg.TokenPrice = fields.TokenPrice
	}
	if fields.CreateDevshardFee > 0 {
		cfg.CreateDevshardFee = fields.CreateDevshardFee
	}
	if fields.FeePerNonce > 0 {
		cfg.FeePerNonce = fields.FeePerNonce
	}
	if fields.InferenceSealGraceNonces > 0 {
		cfg.InferenceSealGraceNonces = fields.InferenceSealGraceNonces
	}
	if fields.InferenceSealGraceSeconds > 0 {
		cfg.InferenceSealGraceSeconds = fields.InferenceSealGraceSeconds
	}
	if fields.AutoSealEveryNNonces > 0 {
		cfg.AutoSealEveryNNonces = fields.AutoSealEveryNNonces
	}
	if fields.ValidationRate > 0 {
		cfg.ValidationRate = fields.ValidationRate
	}
	cfg.VoteThreshold = ComputeVoteThreshold(groupSize, fields.VoteThresholdFactor)
	return NormalizeSessionConfig(cfg, groupSize)
}

// SessionConfigWithPrice returns a session config with a custom token price.
// tokenPrice == 0 is treated as 1 for backward compatibility.
//
// Deprecated: use SessionConfigFromEscrow with EscrowSessionFields{TokenPrice:
// tokenPrice}. Kept as a thin wrapper so existing callers (tests, transitional
// code) keep compiling while phase 1 of session-config-flow-plan.md lands.
func SessionConfigWithPrice(groupSize int, tokenPrice uint64) SessionConfig {
	return SessionConfigFromEscrow(groupSize, EscrowSessionFields{TokenPrice: tokenPrice})
}
