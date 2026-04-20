package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultSessionConfig(t *testing.T) {
	cfg := DefaultSessionConfig(6)
	assert.Equal(t, int64(60), cfg.RefusalTimeout)
	assert.Equal(t, int64(1200), cfg.ExecutionTimeout)
	assert.Equal(t, uint64(1), cfg.TokenPrice)
	assert.Equal(t, uint32(3), cfg.VoteThreshold)
	assert.Equal(t, uint32(5000), cfg.ValidationRate)
}

func TestSessionConfigWithPrice(t *testing.T) {
	t.Run("overrides non-zero price", func(t *testing.T) {
		cfg := SessionConfigWithPrice(4, 42)
		assert.Equal(t, uint64(42), cfg.TokenPrice)
		// other fields remain at defaults
		assert.Equal(t, int64(60), cfg.RefusalTimeout)
		assert.Equal(t, int64(1200), cfg.ExecutionTimeout)
		assert.Equal(t, uint32(5000), cfg.ValidationRate)
	})

	t.Run("zero price keeps default", func(t *testing.T) {
		cfg := SessionConfigWithPrice(4, 0)
		assert.Equal(t, uint64(1), cfg.TokenPrice)
	})
}

func TestSessionConfigFromEscrow(t *testing.T) {
	t.Run("all values from escrow", func(t *testing.T) {
		cfg := SessionConfigFromEscrow(6, 99, 30, 600, 8000)
		assert.Equal(t, uint64(99), cfg.TokenPrice)
		assert.Equal(t, int64(30), cfg.RefusalTimeout)
		assert.Equal(t, int64(600), cfg.ExecutionTimeout)
		assert.Equal(t, uint32(8000), cfg.ValidationRate)
		assert.Equal(t, uint32(3), cfg.VoteThreshold) // groupSize/2
	})

	t.Run("zero values fall back to defaults", func(t *testing.T) {
		cfg := SessionConfigFromEscrow(4, 0, 0, 0, 0)
		assert.Equal(t, uint64(1), cfg.TokenPrice)
		assert.Equal(t, int64(60), cfg.RefusalTimeout)
		assert.Equal(t, int64(1200), cfg.ExecutionTimeout)
		assert.Equal(t, uint32(5000), cfg.ValidationRate)
	})

	t.Run("partial override", func(t *testing.T) {
		cfg := SessionConfigFromEscrow(6, 50, 0, 900, 0)
		assert.Equal(t, uint64(50), cfg.TokenPrice)
		assert.Equal(t, int64(60), cfg.RefusalTimeout)    // default
		assert.Equal(t, int64(900), cfg.ExecutionTimeout)  // overridden
		assert.Equal(t, uint32(5000), cfg.ValidationRate)  // default
	})

	t.Run("backward compat with SessionConfigWithPrice", func(t *testing.T) {
		// When all new fields are zero, result must match SessionConfigWithPrice
		fromEscrow := SessionConfigFromEscrow(6, 42, 0, 0, 0)
		fromPrice := SessionConfigWithPrice(6, 42)
		assert.Equal(t, fromPrice, fromEscrow)
	})
}
