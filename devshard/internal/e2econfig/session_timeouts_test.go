package e2econfig

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestSessionTimeoutOverridesFromEnv(t *testing.T) {
	t.Setenv("DEVSHARD_E2E", "1")
	t.Setenv(RefusalTimeoutSecondsEnv, "2")
	t.Setenv(ExecutionTimeoutSecondsEnv, "3")

	overrides, err := SessionTimeoutOverridesFromEnv()
	config := overrides.Apply(types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
	})

	require.NoError(t, err)
	require.NotNil(t, overrides.RefusalTimeoutSeconds)
	require.NotNil(t, overrides.ExecutionTimeoutSeconds)
	require.EqualValues(t, 2, config.RefusalTimeout)
	require.EqualValues(t, 3, config.ExecutionTimeout)
}

func TestSessionTimeoutOverridesFromEnvRequiresE2E(t *testing.T) {
	t.Setenv(RefusalTimeoutSecondsEnv, "1")

	_, err := SessionTimeoutOverridesFromEnv()

	require.ErrorContains(t, err, "only supported when DEVSHARD_E2E=1")
}

func TestSessionTimeoutOverridesFromEnvRejectsInvalidValues(t *testing.T) {
	t.Setenv("DEVSHARD_E2E", "1")

	t.Run("non numeric", func(t *testing.T) {
		t.Setenv(RefusalTimeoutSecondsEnv, "soon")
		_, err := SessionTimeoutOverridesFromEnv()
		require.ErrorContains(t, err, "invalid "+RefusalTimeoutSecondsEnv)
	})

	t.Run("negative", func(t *testing.T) {
		t.Setenv(RefusalTimeoutSecondsEnv, "-1")
		_, err := SessionTimeoutOverridesFromEnv()
		require.ErrorContains(t, err, "must be non-negative")
	})
}

func TestDurationMillisFromEnv(t *testing.T) {
	t.Setenv("DEVSHARD_E2E", "1")
	t.Setenv(ReceiptDelayMillisEnv, "250")

	value, err := DurationMillisFromEnv(ReceiptDelayMillisEnv)

	require.NoError(t, err)
	require.Equal(t, 250*time.Millisecond, value)
}

func TestDurationMillisFromEnvAllowsUnset(t *testing.T) {
	value, err := DurationMillisFromEnv(ReceiptDelayMillisEnv)

	require.NoError(t, err)
	require.Zero(t, value)
}
