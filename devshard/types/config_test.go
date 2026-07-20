package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultInferenceSealGraceNonces(t *testing.T) {
	t.Run("floor", func(t *testing.T) {
		require.Equal(t, uint32(20), DefaultInferenceSealGraceNonces(0))
		require.Equal(t, uint32(20), DefaultInferenceSealGraceNonces(1))
	})

	t.Run("scaled", func(t *testing.T) {
		require.Equal(t, uint32(20), DefaultInferenceSealGraceNonces(3))
	})
}

func TestNormalizeSessionConfig_FillsInferenceSealGraceNoncesOnlyWhenUnset(t *testing.T) {
	cfg := NormalizeSessionConfig(SessionConfig{
		RefusalTimeout:   7,
		ExecutionTimeout: 9,
		TokenPrice:       11,
		ValidationRate:   1234,
	}, 4)

	require.Equal(t, uint32(20), cfg.InferenceSealGraceNonces)
	require.Equal(t, uint32(DefaultInferenceSealGraceSeconds), cfg.InferenceSealGraceSeconds)
	require.Equal(t, DefaultAutoSealEveryNNonces, cfg.AutoSealEveryNNonces)
	require.Equal(t, int64(7), cfg.RefusalTimeout)
	require.Equal(t, int64(9), cfg.ExecutionTimeout)
	require.Equal(t, uint64(11), cfg.TokenPrice)
	require.Equal(t, uint32(1234), cfg.ValidationRate)

	explicit := NormalizeSessionConfig(SessionConfig{InferenceSealGraceNonces: 77}, 4)
	require.Equal(t, uint32(77), explicit.InferenceSealGraceNonces)

	explicitClear := NormalizeSessionConfig(SessionConfig{InferenceSealGraceSeconds: 45}, 4)
	require.Equal(t, uint32(45), explicitClear.InferenceSealGraceSeconds)
	require.Equal(t, uint32(20), explicitClear.InferenceSealGraceNonces)
}

func TestSessionConfigFromEscrow_ZeroFallback(t *testing.T) {
	const groupSize = 4
	require.Equal(t,
		DefaultSessionConfig(groupSize),
		SessionConfigFromEscrow(groupSize, EscrowSessionFields{}),
	)
}

func TestSessionConfigFromEscrow_PerFieldOverride(t *testing.T) {
	const groupSize = 4

	t.Run("token price only", func(t *testing.T) {
		base := DefaultSessionConfig(groupSize)
		got := SessionConfigFromEscrow(groupSize, EscrowSessionFields{TokenPrice: 42})

		require.Equal(t, uint64(42), got.TokenPrice)
		require.Equal(t, base.CreateDevshardFee, got.CreateDevshardFee)
		require.Equal(t, base.FeePerNonce, got.FeePerNonce)
		require.Equal(t, base.InferenceSealGraceNonces, got.InferenceSealGraceNonces)
		require.Equal(t, base.InferenceSealGraceSeconds, got.InferenceSealGraceSeconds)
		require.Equal(t, base.ValidationRate, got.ValidationRate)
		require.Equal(t, base.VoteThreshold, got.VoteThreshold)
	})

	t.Run("create devshard fee only", func(t *testing.T) {
		base := DefaultSessionConfig(groupSize)
		got := SessionConfigFromEscrow(groupSize, EscrowSessionFields{CreateDevshardFee: 12_345})

		require.Equal(t, uint64(12_345), got.CreateDevshardFee)
		require.Equal(t, base.TokenPrice, got.TokenPrice)
		require.Equal(t, base.FeePerNonce, got.FeePerNonce)
	})

	t.Run("fee per nonce only", func(t *testing.T) {
		base := DefaultSessionConfig(groupSize)
		got := SessionConfigFromEscrow(groupSize, EscrowSessionFields{FeePerNonce: 9_999})

		require.Equal(t, uint64(9_999), got.FeePerNonce)
		require.Equal(t, base.TokenPrice, got.TokenPrice)
		require.Equal(t, base.CreateDevshardFee, got.CreateDevshardFee)
	})

	t.Run("all three", func(t *testing.T) {
		got := SessionConfigFromEscrow(groupSize, EscrowSessionFields{
			TokenPrice:        7,
			CreateDevshardFee: 13,
			FeePerNonce:       19,
		})

		require.Equal(t, uint64(7), got.TokenPrice)
		require.Equal(t, uint64(13), got.CreateDevshardFee)
		require.Equal(t, uint64(19), got.FeePerNonce)
	})
}

func TestComputeVoteThreshold(t *testing.T) {
	require.Equal(t, uint32(3), ComputeVoteThreshold(6, 0))
	require.Equal(t, uint32(3), ComputeVoteThreshold(6, 50))
	require.Equal(t, uint32(4), ComputeVoteThreshold(6, 67))
	require.Equal(t, uint32(6), ComputeVoteThreshold(6, 100))
}

func TestSessionConfigFromEscrow_ConsensusFieldsFromEscrow(t *testing.T) {
	const groupSize = 6
	cfg := SessionConfigFromEscrow(groupSize, EscrowSessionFields{
		InferenceSealGraceNonces:  55,
		InferenceSealGraceSeconds: 99,
		ValidationRate:            6000,
		VoteThresholdFactor:       67,
	})
	require.Equal(t, uint32(6000), cfg.ValidationRate)
	require.Equal(t, uint32(55), cfg.InferenceSealGraceNonces)
	require.Equal(t, uint32(99), cfg.InferenceSealGraceSeconds)
	require.Equal(t, uint32(4), cfg.VoteThreshold)
}

func TestSessionConfigFromEscrow_PreservesDefaultValidationRateWhenZero(t *testing.T) {
	const groupSize = 16
	cfg := SessionConfigFromEscrow(groupSize, EscrowSessionFields{
		InferenceSealGraceNonces:  1,
		InferenceSealGraceSeconds: 10,
	})
	require.Equal(t, uint32(5000), cfg.ValidationRate)
	require.Equal(t, uint32(1), cfg.InferenceSealGraceNonces)
	require.Equal(t, uint32(10), cfg.InferenceSealGraceSeconds)
}

func TestSessionConfigWithPrice_WrapsBuilder(t *testing.T) {
	const groupSize = 4

	t.Run("zero price falls back to default", func(t *testing.T) {
		require.Equal(t,
			SessionConfigFromEscrow(groupSize, EscrowSessionFields{}),
			SessionConfigWithPrice(groupSize, 0),
		)
	})

	t.Run("non-zero price matches token-price override", func(t *testing.T) {
		const price uint64 = 31
		require.Equal(t,
			SessionConfigFromEscrow(groupSize, EscrowSessionFields{TokenPrice: price}),
			SessionConfigWithPrice(groupSize, price),
		)
	})
}
