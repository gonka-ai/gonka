package types

// DefaultDevshardMaxNonce mirrors the chain-side
// DefaultDevshardMaxNonce constant used by DevshardEscrowParams. Keep these
// two values in sync; the host falls back to this value whenever the escrow
// reports zero so that pre-v0.2.13 escrows stay capped.
const DefaultDevshardMaxNonce uint32 = 20_000

// DefaultSessionConfig returns the canonical session config that both user and
// host must use. A single source of truth prevents state root divergence caused
// by config mismatches (e.g. different ValidationRate values).
func DefaultSessionConfig(groupSize int) SessionConfig {
	return SessionConfig{
		RefusalTimeout:    60,
		ExecutionTimeout:  1200,
		TokenPrice:        1,
		CreateDevshardFee: 10_000,
		FeePerNonce:       1_000,
		VoteThreshold:     uint32(groupSize) / 2,
		ValidationRate:    5000,
		MaxNonce:          DefaultDevshardMaxNonce,
	}
}

// EscrowSessionFields holds the per-escrow session-config values resolved from
// DevshardEscrow at creation time. Each field, when zero, falls back to the
// compiled DefaultSessionConfig value in SessionConfigFromEscrow — this keeps
// chains created before a given governance field existed working unchanged.
type EscrowSessionFields struct {
	TokenPrice        uint64
	RefusalTimeout    int64
	ExecutionTimeout  int64
	ValidationRate    uint32
	CreateDevshardFee uint64
	FeePerNonce       uint64
	MaxNonce          uint32
}

// SessionConfigFromEscrow builds a SessionConfig from raw devshard-escrow
// fields. Any field equal to zero falls back to the compiled default from
// DefaultSessionConfig, preserving backward compatibility with escrows
// created before these governance parameters existed.
func SessionConfigFromEscrow(groupSize int, fields EscrowSessionFields) SessionConfig {
	cfg := DefaultSessionConfig(groupSize)
	if fields.TokenPrice > 0 {
		cfg.TokenPrice = fields.TokenPrice
	}
	if fields.RefusalTimeout > 0 {
		cfg.RefusalTimeout = fields.RefusalTimeout
	}
	if fields.ExecutionTimeout > 0 {
		cfg.ExecutionTimeout = fields.ExecutionTimeout
	}
	if fields.ValidationRate > 0 {
		cfg.ValidationRate = fields.ValidationRate
	}
	if fields.CreateDevshardFee > 0 {
		cfg.CreateDevshardFee = fields.CreateDevshardFee
	}
	if fields.FeePerNonce > 0 {
		cfg.FeePerNonce = fields.FeePerNonce
	}
	if fields.MaxNonce > 0 {
		cfg.MaxNonce = fields.MaxNonce
	}
	return cfg
}

// SessionConfigWithPrice returns a session config with a custom token price.
// Deprecated: use SessionConfigFromEscrow. Retained for callers that have not
// yet been updated to thread the additional governance fields.
func SessionConfigWithPrice(groupSize int, tokenPrice uint64) SessionConfig {
	return SessionConfigFromEscrow(groupSize, EscrowSessionFields{TokenPrice: tokenPrice})
}
