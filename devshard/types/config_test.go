package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultSealGraceNonces(t *testing.T) {
	t.Run("floor", func(t *testing.T) {
		require.Equal(t, uint32(20), DefaultSealGraceNonces(0))
		require.Equal(t, uint32(20), DefaultSealGraceNonces(1))
	})

	t.Run("scaled", func(t *testing.T) {
		require.Equal(t, uint32(30), DefaultSealGraceNonces(3))
	})
}

func TestNormalizeSessionConfig_FillsSealGraceNoncesOnlyWhenUnset(t *testing.T) {
	cfg := NormalizeSessionConfig(SessionConfig{
		RefusalTimeout:   7,
		ExecutionTimeout: 9,
		TokenPrice:       11,
		ValidationRate:   1234,
	}, 4)

	require.Equal(t, uint32(40), cfg.SealGraceNonces)
	require.Equal(t, int64(7), cfg.RefusalTimeout)
	require.Equal(t, int64(9), cfg.ExecutionTimeout)
	require.Equal(t, uint64(11), cfg.TokenPrice)
	require.Equal(t, uint32(1234), cfg.ValidationRate)

	explicit := NormalizeSessionConfig(SessionConfig{SealGraceNonces: 77}, 4)
	require.Equal(t, uint32(77), explicit.SealGraceNonces)
}
