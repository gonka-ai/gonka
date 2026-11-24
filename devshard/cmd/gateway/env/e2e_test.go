package env

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/e2econfig"
)

// Test flow:
//  1. Declare the stand as e2e and set both the refusal and execution timeout environment variables.
//  2. Call `LoadE2E`.
//  3. Assert both session timeouts are loaded with their configured values.
func TestLoadE2EReadsTheProtocolDeadlines(t *testing.T) {
	t.Setenv("DEVSHARD_E2E", "1")
	t.Setenv(e2econfig.RefusalTimeoutSecondsEnv, "5")
	t.Setenv(e2econfig.ExecutionTimeoutSecondsEnv, "10")

	loaded := LoadE2E()

	require.NotNil(t, loaded.SessionTimeouts.RefusalTimeoutSeconds)
	require.Equal(t, int64(5), *loaded.SessionTimeouts.RefusalTimeoutSeconds)
	require.NotNil(t, loaded.SessionTimeouts.ExecutionTimeoutSeconds)
	require.Equal(t, int64(10), *loaded.SessionTimeouts.ExecutionTimeoutSeconds)
}

// Test flow:
//  1. Set the execution timeout environment variable without declaring the stand as e2e.
//  2. Call `LoadE2E`.
//  3. Assert the execution timeout stays nil, since a stand that declares nothing is production and must read no override.
func TestLoadE2EWithoutADeclarationReadsNoDeadline(t *testing.T) {
	t.Setenv(e2econfig.ExecutionTimeoutSecondsEnv, "10")

	loaded := LoadE2E()

	require.Nil(t, loaded.SessionTimeouts.ExecutionTimeoutSeconds)
}
