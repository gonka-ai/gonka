package types

// DefaultSessionConfig returns the normal v1 session config.
func DefaultSessionConfig(groupSize int) SessionConfig {
	return SessionConfig{
		RefusalTimeout:    60,
		ExecutionTimeout:  1200,
		TokenPrice:        1,
		CreateDevshardFee: 10_000,
		FeePerNonce:       1_000,
		VoteThreshold:     uint32(groupSize) / 2,
		ValidationRate:    5000,
	}
}

// DefaultSessionConfigV0211 returns the legacy v0.2.11 session config without fees.
func DefaultSessionConfigV0211(groupSize int) SessionConfig {
	cfg := DefaultSessionConfig(groupSize)
	cfg.CreateDevshardFee = 0
	cfg.FeePerNonce = 0
	return cfg
}

// DefaultSessionConfigV1 returns the v1 session config with fee fields.
func DefaultSessionConfigV1(groupSize int) SessionConfig {
	cfg := DefaultSessionConfig(groupSize)
	return cfg
}

// SessionConfigForVersion returns the default config for the given protocol version.
func SessionConfigForVersion(groupSize int, version ProtocolVersion) SessionConfig {
	switch version {
	case ProtocolV0211:
		return DefaultSessionConfigV0211(groupSize)
	default:
		return DefaultSessionConfigV1(groupSize)
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

// SessionConfigWithPriceAndVersion returns a versioned session config with a custom token price.
func SessionConfigWithPriceAndVersion(groupSize int, tokenPrice uint64, version ProtocolVersion) SessionConfig {
	cfg := SessionConfigForVersion(groupSize, version)
	if tokenPrice > 0 {
		cfg.TokenPrice = tokenPrice
	}
	return cfg
}
