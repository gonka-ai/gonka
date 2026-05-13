package types

const (
	defaultSealGraceMultiplier = 10
	minSealGraceNonces         = 20
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

// SessionConfigWithPrice returns a session config with a custom token price.
// tokenPrice == 0 is treated as 1 for backward compatibility.
func SessionConfigWithPrice(groupSize int, tokenPrice uint64) SessionConfig {
	cfg := DefaultSessionConfig(groupSize)
	if tokenPrice > 0 {
		cfg.TokenPrice = tokenPrice
	}
	return cfg
}
