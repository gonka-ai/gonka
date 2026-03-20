package types

// DefaultSessionConfig returns the canonical session config that both user and
// host must use. A single source of truth prevents state root divergence caused
// by config mismatches (e.g. different ValidationRate values).
func DefaultSessionConfig(groupSize int) SessionConfig {
	return SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    uint32(groupSize) / 2,
		ValidationRate:   5000,
	}
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
