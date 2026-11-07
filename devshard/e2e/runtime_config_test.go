package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
	"devshard/types"
)

// Test flow:
//  1. Start the environment once on the legacy wall clock and once on the stamp clock.
//  2. Read runtime status from devshardctl.
//  3. Wait for the mock-chain inference phase to be reported.
//  4. Assert the status identifies the seeded escrow and a balance that
//     subtracts the create fee and one fee for every applied nonce.
//  5. Assert every deliberately non-default escrow configuration field is
//     applied from mock-chain, including which timeout clock is bound.
func TestE2E_ChainBackedRuntimeConfigStatus(t *testing.T) {
	for _, header := range []bool{false, true} {
		t.Run(timeoutClockSubtest(header), func(t *testing.T) {
			runChainBackedRuntimeConfigStatus(t, header)
		})
	}
}

func runChainBackedRuntimeConfigStatus(t *testing.T, header bool) {
	t.Helper()
	env, client := startNonStreamingEnvWithOptions(t, withHeightsyncTimeoutClock(e2eEnvOptions{}, header))
	requireHostTimeoutClock(t, client, env, header)

	var status map[string]any
	require.Eventually(t, func() bool {
		status = testutil.GetStatus(t, client, env.clientURL)
		return status["chain_phase"] == "Inference"
	}, 10*time.Second, 100*time.Millisecond, "devshardctl should report the mock-chain inference phase")

	require.Equal(t, defaultEscrowID, status["escrow_id"])
	require.Equal(t, "active", status["phase"])
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
	require.Equal(t, header, config["heightsync_timeout_clock"] == true, "status must report the bound timeout clock")

	// Heartbeats are applied nonces and each one charges fee_per_nonce, so the
	// balance moves with the nonce on this same status snapshot.
	nonce := testutil.NumericField(t, status, "nonce")
	createFee := testutil.NumericField(t, config, "create_devshard_fee")
	feePerNonce := testutil.NumericField(t, config, "fee_per_nonce")
	require.Equal(t, uint64(1_000_000)-createFee-feePerNonce*nonce, testutil.NumericField(t, status, "balance"),
		"balance should be the seed minus the create fee and one fee per applied nonce")
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
