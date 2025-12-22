package payloadstorage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPayloadStorage_RetryInterval(t *testing.T) {
	// Set PGHOST to ensure we get a HybridStorage (or attempt to)
	os.Setenv("PGHOST", "localhost")
	defer os.Unsetenv("PGHOST")

	tests := []struct {
		name         string
		envValue     string
		expected     time.Duration
		expectHybrid bool
	}{
		{
			name:         "Default (unset)",
			envValue:     "",
			expected:     240 * time.Second,
			expectHybrid: true,
		},
		{
			name:         "Custom valid duration",
			envValue:     "10s",
			expected:     10 * time.Second,
			expectHybrid: true,
		},
		{
			name:         "Invalid duration (fallback to default)",
			envValue:     "invalid",
			expected:     240 * time.Second,
			expectHybrid: true,
		},
		{
			name:         "Zero duration (fallback to default)",
			envValue:     "0s",
			expected:     240 * time.Second,
			expectHybrid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envValue != "" {
				os.Setenv("PG_RETRY_INTERVAL", tt.envValue)
				defer os.Unsetenv("PG_RETRY_INTERVAL")
			} else {
				os.Unsetenv("PG_RETRY_INTERVAL")
			}

			// We expect NewPostgresStorage to fail (we aren't running a real DB here usually,
			// or if we are, we don't care, we just want to check the struct construction).
			// If NewPostgresStorage fails, it returns a HybridStorage with nil PG.
			// If it succeeds, it returns a HybridStorage with real PG.
			// In both cases, we get a HybridStorage.

			// Note: NewPostgresStorage might fail fast if connection fails.
			// Factory handles error by logging and returning HybridStorage(nil, file).

			s := NewPayloadStorage(context.Background(), t.TempDir())

			if tt.expectHybrid {
				hs, ok := s.(*HybridStorage)
				require.True(t, ok, "Expected *HybridStorage")
				assert.Equal(t, tt.expected, hs.retryInterval)
			}
		})
	}
}

