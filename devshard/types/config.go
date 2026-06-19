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
		ValidationRate:    5000,
	}, groupSize)
}

// EscrowSessionFields collects per-escrow parameters frozen onto DevshardEscrow
// at create. Every field is "zero means use the compiled default" so callers can
// populate only what the chain returned.
type EscrowSessionFields struct {
	TokenPrice                uint64
	CreateDevshardFee         uint64
	FeePerNonce               uint64
	InferenceSealGraceNonces  uint32
	InferenceSealGraceSeconds uint32
	AutoSealEveryNNonces      uint32
}

// LiveSessionBindParams carries governance fields read from the long-poll
// snapshot once at session bind. Zero means "not provided" for that field.
type LiveSessionBindParams struct {
	RefusalTimeout      int64
	ExecutionTimeout    int64
	ValidationRate      uint32
	VoteThresholdFactor uint32 // percent, e.g. 50 == 50%
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

// ApplyLiveSessionParams overlays live governance fields onto cfg and applies
// NormalizeSessionConfig. Call after SessionConfigFromEscrow at bind time.
func ApplyLiveSessionParams(cfg SessionConfig, groupSize int, live LiveSessionBindParams) SessionConfig {
	if live.ValidationRate > 0 {
		cfg.ValidationRate = live.ValidationRate
	}
	cfg.VoteThreshold = ComputeVoteThreshold(groupSize, live.VoteThresholdFactor)
	if live.RefusalTimeout > 0 {
		cfg.RefusalTimeout = live.RefusalTimeout
	}
	if live.ExecutionTimeout > 0 {
		cfg.ExecutionTimeout = live.ExecutionTimeout
	}
	return NormalizeSessionConfig(cfg, groupSize)
}

// ApplyChainSessionBindParams overlays lane-B fields from a chain Params query
// at session bind. Unlike ApplyLiveSessionParams, validation_rate=0 from chain
// is honored (disables validation sampling).
func ApplyChainSessionBindParams(cfg SessionConfig, groupSize int, live LiveSessionBindParams) SessionConfig {
	cfg.ValidationRate = live.ValidationRate
	cfg.VoteThreshold = ComputeVoteThreshold(groupSize, live.VoteThresholdFactor)
	if live.RefusalTimeout > 0 {
		cfg.RefusalTimeout = live.RefusalTimeout
	}
	if live.ExecutionTimeout > 0 {
		cfg.ExecutionTimeout = live.ExecutionTimeout
	}
	return NormalizeSessionConfig(cfg, groupSize)
}

// SessionConfigFromEscrow builds a SessionConfig by starting from the
// compiled DefaultSessionConfig and overlaying any non-zero per-escrow values.
// Live (long-poll) lane-B fields are layered on by the caller after this
// returns, before NormalizeSessionConfig finalizes derived defaults.
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
