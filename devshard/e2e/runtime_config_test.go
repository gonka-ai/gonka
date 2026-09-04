package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
	"devshard/types"
)

// Test flow:
//  1. Read runtime status from devshardctl.
//  2. Wait for the mock-chain inference phase to be reported.
//  3. Assert the status identifies the seeded escrow and its initial balance.
//  4. Assert every deliberately non-default escrow configuration field is
//     applied from mock-chain.
func TestE2E_ChainBackedRuntimeConfigStatus(t *testing.T) {
	env, client := startNonStreamingEnv(t)

	var status map[string]any
	require.Eventually(t, func() bool {
		status = testutil.GetStatus(t, client, env.clientURL)
		return status["chain_phase"] == "Inference"
	}, 10*time.Second, 100*time.Millisecond, "devshardctl should report the mock-chain inference phase")

	require.Equal(t, defaultEscrowID, status["escrow_id"])
	require.Equal(t, "active", status["phase"])
	require.Equal(t, uint64(1_000_000-12_345), testutil.NumericField(t, status, "balance"))
	requestsBlocked, ok := status["requests_blocked"].(bool)
	require.True(t, ok, "requests_blocked should be a boolean")
	require.False(t, requestsBlocked, "mock-chain inference phase should admit requests")

	config, ok := status["config"].(map[string]any)
	require.True(t, ok, "status config should be an object")
	for field, want := range map[string]uint64{
		"token_price":                  7,
		"create_devshard_fee":          12_345,
		"fee_per_nonce":                19,
		"vote_threshold":               1,
		"validation_rate":              10_000,
		"inference_seal_grace_nonces":  9,
		"inference_seal_grace_seconds": 77,
		"auto_seal_every_n_nonces":     21,
	} {
		require.Equalf(t, want, testutil.NumericField(t, config, field),
			"status config %s should match the configured mock-chain escrow value", field)
	}
}

// Test flow:
//  1. Start the normal E2E environment with mock-chain timeout fields omitted.
//  2. Read the chain-backed runtime status from devshardctl.
//  3. Assert refusal and execution timeouts equal the canonical protocol
//     defaults rather than a test-specific override.
func TestE2E_ChainBackedRuntimeConfigUsesDefaultTimeouts(t *testing.T) {
	env, client := startNonStreamingEnv(t)

	status := testutil.GetStatus(t, client, env.clientURL)
	config, ok := status["config"].(map[string]any)
	require.True(t, ok, "status config should be an object")

	defaults := types.DefaultSessionConfig(len(testutil.HostPrivateKeys))
	require.Equal(t, uint64(defaults.RefusalTimeout), testutil.NumericField(t, config, "refusal_timeout"))
	require.Equal(t, uint64(defaults.ExecutionTimeout), testutil.NumericField(t, config, "execution_timeout"))
}
