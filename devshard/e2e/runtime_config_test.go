package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
	"devshard/types"
)

const (
	seededEscrowAmount      = 1_000_000
	seededCreateDevshardFee = 12_345
	chainPhaseInference     = "Inference"
	escrowPhaseActiveName   = "active"
)

// Test flow:
//  1. Read runtime status from the gateway until it reports the mock-chain inference phase.
//  2. Assert the status admits requests.
//  3. Read the escrow's debug state and assert it identifies the seeded escrow, active, with a balance no higher than the seeded amount less the create fee.
//  4. Assert every deliberately non-default escrow configuration field is applied from mock-chain.
func TestE2E_ChainBackedRuntimeConfigStatus(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	var status map[string]any
	require.Eventually(t, func() bool {
		status = testutil.GetJSON(t, client, env.clientURL+"/v1/status")
		return status["chain_phase"] == chainPhaseInference
	}, 10*time.Second, 100*time.Millisecond, "the gateway should report the mock-chain inference phase")
	requestsBlocked, ok := status["requests_blocked"].(bool)
	require.True(t, ok, "requests_blocked should be a boolean")
	require.False(t, requestsBlocked, "mock-chain inference phase should admit requests")

	state := testutil.GetJSON(t, client, env.escrowURL()+testutil.DebugStateRoute)
	require.Equal(t, defaultEscrowID, state["escrow_id"])
	require.Equal(t, escrowPhaseActiveName, state["phase"])
	balance := testutil.NumericField(t, state, "balance")
	require.NotZero(t, balance, "the seeded escrow should hold a balance")
	require.LessOrEqual(t, balance, uint64(seededEscrowAmount-seededCreateDevshardFee),
		"the balance should start from the seeded amount less the create fee; the startup warm-up may already have spent from it")

	config, ok := state["config"].(map[string]any)
	require.True(t, ok, "debug state config should be an object")
	for field, want := range map[string]uint64{
		"TokenPrice":                7,
		"CreateDevshardFee":         seededCreateDevshardFee,
		"FeePerNonce":               19,
		"VoteThreshold":             1,
		"ValidationRate":            10_000,
		"InferenceSealGraceNonces":  9,
		"InferenceSealGraceSeconds": 77,
		"AutoSealEveryNNonces":      21,
	} {
		require.Equalf(t, want, testutil.NumericField(t, config, field),
			"debug state config %s should match the configured mock-chain escrow value", field)
	}
}

// Test flow:
//  1. Start the normal E2E environment with mock-chain timeout fields omitted.
//  2. Read the escrow's debug state from the gateway.
//  3. Assert refusal and execution timeouts equal the canonical protocol defaults rather than a test-specific override.
func TestE2E_ChainBackedRuntimeConfigUsesDefaultTimeouts(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	state := testutil.GetJSON(t, client, env.escrowURL()+testutil.DebugStateRoute)
	config, ok := state["config"].(map[string]any)
	require.True(t, ok, "debug state config should be an object")

	defaults := types.DefaultSessionConfig(len(testutil.HostPrivateKeys))
	require.Equal(t, uint64(defaults.RefusalTimeout), testutil.NumericField(t, config, "RefusalTimeout"))
	require.Equal(t, uint64(defaults.ExecutionTimeout), testutil.NumericField(t, config, "ExecutionTimeout"))
}
