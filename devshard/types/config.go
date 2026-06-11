package types

const (
	// defaultSealGraceMultiplier        = 10
	// minSealGraceNonces                = 20
	// DefaultInferenceClearGraceSeconds = 3600 // 1 hour (give time for long validations)

	defaultSealGraceMultiplier        = 1 // for tests
	minSealGraceNonces                = 20
	DefaultInferenceClearGraceSeconds = 120 // 2 minutes (for tests)
)

// DefaultSealGraceNonces returns the canonical seal grace for a session group.
// Phase 1 uses a nonce gate of 10 * groupSize with a floor of 20 so small
// groups still leave enough room for post-terminal traffic before sealing.
func DefaultSealGraceNonces(groupSize int) uint32 {
	grace := groupSize * defaultSealGraceMultiplier
	if grace < minSealGraceNonces {
		grace = minSealGraceNonces
	}
	return uint32(grace)
}

// NormalizeSessionConfig applies derived defaults that must be fixed once a
// session is created. Zero values that have protocol meaning (such as timeout=0)
// are preserved; only fields with explicit "unset means use canonical default"
// semantics are filled here.
func NormalizeSessionConfig(cfg SessionConfig, groupSize int) SessionConfig {
	if cfg.SealGraceNonces == 0 {
		cfg.SealGraceNonces = DefaultSealGraceNonces(groupSize)
	}
	if cfg.InferenceClearGraceSeconds == 0 {
		cfg.InferenceClearGraceSeconds = DefaultInferenceClearGraceSeconds
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

// EscrowSessionFields collects the per-escrow fee parameters that are frozen
// onto DevshardEscrow at create. Every field is "zero means use the compiled
// default" so callers can populate only what the chain returned.
type EscrowSessionFields struct {
	TokenPrice        uint64
	CreateDevshardFee uint64
	FeePerNonce       uint64
}

// LiveSessionBindParams carries governance fields read from the long-poll
// snapshot once at session bind. Zero means "not provided" for that field.
type LiveSessionBindParams struct {
	RefusalTimeout             int64
	ExecutionTimeout           int64
	ValidationRate             uint32
	SealGraceNonces            uint32
	InferenceClearGraceSeconds uint32
	VoteThresholdFactor        uint32 // percent, e.g. 50 == 50%
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
	if live.SealGraceNonces > 0 {
		cfg.SealGraceNonces = live.SealGraceNonces
	}
	if live.InferenceClearGraceSeconds > 0 {
		cfg.InferenceClearGraceSeconds = live.InferenceClearGraceSeconds
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
// is honored (disables validation sampling). Seal/clear grace zero values still
// fall through to NormalizeSessionConfig defaults.
func ApplyChainSessionBindParams(cfg SessionConfig, groupSize int, live LiveSessionBindParams) SessionConfig {
	cfg.ValidationRate = live.ValidationRate
	if live.SealGraceNonces > 0 {
		cfg.SealGraceNonces = live.SealGraceNonces
	}
	if live.InferenceClearGraceSeconds > 0 {
		cfg.InferenceClearGraceSeconds = live.InferenceClearGraceSeconds
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

// SessionConfigFromEscrow builds a SessionConfig by starting from the
// compiled DefaultSessionConfig and overlaying any non-zero per-escrow fee
// values. This is the canonical constructor for "bind a session against an
// on-chain escrow"; live (long-poll) fields are layered on by the caller after
// this returns, before NormalizeSessionConfig finalizes derived defaults.
//
// Zero fields fall through to defaults so legacy escrows (no fee snapshot)
// keep today's behavior — see devshard/docs/session-config-flow-plan.md.
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
	return cfg
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
