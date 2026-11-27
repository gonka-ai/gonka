package e2e

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
	"devshard/internal/e2econfig"
	"devshard/types"
)

const (
	timeoutProtocolRefusalSeconds   = 5
	timeoutProtocolExecutionSeconds = 17
	stubExecutionHangEnv            = "DEVSHARD_STUB_EXECUTION_HANG"
	timeoutProtocolTrafficRequests  = 3
	allHostsHangAnswerWithin        = time.Minute
	refusalTimeoutLandsWithin       = 40 * time.Second
	executionTimeoutLandsWithin     = 60 * time.Second
	noSkippedSlot                   = -1
	e2eModeEnv                      = "DEVSHARD_E2E"
	e2eModeOn                       = "1"
	mockChainConfigEnv              = "MOCK_CHAIN_CONFIG"
	timeoutMockChainConfig          = "/app/mock-chain-timeout-config.yaml"
)

func timeoutProtocolEnvOptions() e2eEnvOptions {
	return e2eEnvOptions{
		gatewayEnvOverrides: map[string]string{
			e2eModeEnv:                           e2eModeOn,
			e2econfig.RefusalTimeoutSecondsEnv:   fmt.Sprint(timeoutProtocolRefusalSeconds),
			e2econfig.ExecutionTimeoutSecondsEnv: fmt.Sprint(timeoutProtocolExecutionSeconds),
		},
		hostEnv: map[string]string{
			"DEVSHARD_REFUSAL_TIMEOUT":   fmt.Sprint(timeoutProtocolRefusalSeconds),
			"DEVSHARD_EXECUTION_TIMEOUT": fmt.Sprint(timeoutProtocolExecutionSeconds),
		},
		mockChainEnv: map[string]string{
			mockChainConfigEnv: timeoutMockChainConfig,
		},
	}
}

// awaitTimedOutInference polls the escrow until an inference executed by executorSlot times out, and returns it.
func awaitTimedOutInference(t *testing.T, client *http.Client, escrowURL string, executorSlot uint64, within time.Duration) map[string]any {
	t.Helper()
	var found map[string]any
	timedOut := assert.Eventually(t, func() bool {
		for _, inference := range testutil.TimedOutInferences(t, client, escrowURL) {
			record, ok := inference["record"].(map[string]any)
			if ok && testutil.NumericField(t, record, "executor_slot") == executorSlot {
				found = inference
				return true
			}
		}
		return false
	}, within, time.Second, "an inference executed by slot %d should time out", executorSlot)
	require.True(t, timedOut, "an inference executed by slot %d should time out: %v", executorSlot,
		testutil.GetJSON(t, client, escrowURL+testutil.DebugInferencesRoute))
	return found
}

// awaitTimeoutTransaction polls the hosts' stored diffs until one carries a MsgTimeoutInference.
func awaitTimeoutTransaction(t *testing.T, client *http.Client, env *e2eEnv, skippedSlot int) testutil.TimeoutInferenceTransaction {
	t.Helper()
	var timeoutTx testutil.TimeoutInferenceTransaction
	applied := assert.Eventually(t, func() bool {
		latestNonce := gatewaySessionNonce(t, client, env.clientURL)
		for slotID, hostURL := range env.hostControlURLs {
			if slotID == skippedSlot {
				continue
			}
			transaction, found := testutil.FindTimeoutInferenceTransaction(
				t, client, hostURL, e2eHostRoutePrefix, defaultEscrowID, latestNonce,
			)
			if found {
				timeoutTx = transaction
				return true
			}
		}
		return false
	}, 10*time.Second, 250*time.Millisecond, "host storage should include MsgTimeoutInference")
	require.True(t, applied, "host storage should include MsgTimeoutInference")
	return timeoutTx
}

func escrowConfig(t *testing.T, client *http.Client, escrowURL string) map[string]any {
	t.Helper()
	state := testutil.GetJSON(t, client, escrowURL+testutil.DebugStateRoute)
	config, ok := state["config"].(map[string]any)
	require.True(t, ok, "debug state config should be an object")
	return config
}

func slotMisses(t *testing.T, client *http.Client, escrowURL string, slot uint64) uint64 {
	t.Helper()
	for _, stats := range testutil.EscrowHostStatsBySlot(t, client, escrowURL) {
		if stats.Slot == slot {
			return stats.Missed
		}
	}
	t.Fatalf("debug state carries no host stats for slot %d", slot)
	return 0
}

// Test flow:
//  1. Start the default three-host environment on the short-deadline chain config and stop slot 1.
//  2. Send non-streaming completions through the gateway and assert the remaining hosts serve them.
//  3. Wait for the escrow-bound refusal deadline and assert an inference executed by slot 1 becomes timed out.
//  4. Read a live host's stored diffs and assert a MsgTimeoutInference for it was applied with threshold-sufficient votes from the other slots.
//  5. Assert the escrow state marks slot 1 as missed.
//  6. Restart slot 1, continue the session, and finalize successfully.
func TestE2E_TimeoutProtocolEndToEnd(t *testing.T) {
	const unavailableSlot = 1
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	env, client := startGatewayEnv(t, timeoutProtocolEnvOptions())
	require.Equal(t, uint64(timeoutProtocolRefusalSeconds), testutil.NumericField(t, escrowConfig(t, client, env.escrowURL()), "RefusalTimeout"),
		"timeout protocol should use the escrow-bound refusal timeout")
	env.stopHost(ctx, t, unavailableSlot)

	for request := range timeoutProtocolTrafficRequests {
		response := testutil.SendCompletionRaw(t, client, env.clientURL, fmt.Sprintf("timeout protocol %d", request), testutil.AdminAPIKey)
		testutil.LogRawResponse(t, "timeout protocol fallback completion", response)
		testutil.RequireOpenAINonStreamingCompletion(t, response)
	}

	timedOut := awaitTimedOutInference(t, client, env.escrowURL(), unavailableSlot, refusalTimeoutLandsWithin)
	timeoutTx := awaitTimeoutTransaction(t, client, env, unavailableSlot)
	testutil.RequireTimeoutInferenceTransaction(
		t, timeoutTx, testutil.NumericField(t, timedOut, "nonce"), types.TimeoutReason_TIMEOUT_REASON_REFUSED,
		testutil.NumericField(t, escrowConfig(t, client, env.escrowURL()), "VoteThreshold"), unavailableSlot,
	)
	require.GreaterOrEqual(t, slotMisses(t, client, env.escrowURL(), unavailableSlot), uint64(1),
		"timeout should mark the unavailable executor as missed")

	env.hostControlURLs[unavailableSlot] = containerURL(ctx, t, env.startHost(ctx, t, unavailableSlot), "8080/tcp")
	continued := testutil.SendCompletionRaw(t, client, env.clientURL, "timeout protocol continued", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "timeout protocol continued completion", continued)
	testutil.RequireOpenAINonStreamingCompletion(t, continued)

	testutil.DriveUntilValidationObserved(t, client, env.clientURL, env.escrowURL())
	testutil.FinalizeSession(t, client, env.escrowURL())
	testutil.RequireFinalized(t, client, env.escrowURL())
}

// Test flow:
//  1. Start the three-host environment on the short-deadline chain config, with persistent SQLite volumes and the slot 1 stub executor hanging after its receipt is accepted.
//  2. Send non-streaming completions and assert the gateway fails over so the client is served.
//  3. Wait for the escrow-bound execution deadline and assert an inference executed by slot 1 becomes timed out after its receipt.
//  4. Assert the timeout transaction is persisted with TIMEOUT_REASON_EXECUTION and threshold-sufficient votes, and slot 1 is marked missed.
//  5. Restart slot 1 without the hang, continue the session, and finalize.
func TestE2E_ExecutionTimeoutProtocolEndToEnd(t *testing.T) {
	const executorSlot = 1
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	opts := timeoutProtocolEnvOptions()
	opts.hostVolumeNames = sqliteHostVolumeNames(t)
	opts.hostEnvOverrides = map[int]map[string]string{
		executorSlot: {stubExecutionHangEnv: "true"},
	}
	env, client := startGatewayEnv(t, opts)
	require.Equal(t, uint64(timeoutProtocolExecutionSeconds), testutil.NumericField(t, escrowConfig(t, client, env.escrowURL()), "ExecutionTimeout"),
		"timeout protocol should use the escrow-bound execution timeout")

	for request := range timeoutProtocolTrafficRequests {
		response := testutil.SendCompletionRaw(t, client, env.clientURL, fmt.Sprintf("execution timeout protocol %d", request), testutil.AdminAPIKey)
		testutil.LogRawResponse(t, "execution timeout failover completion", response)
		testutil.RequireOpenAINonStreamingCompletion(t, response)
	}

	timedOut := awaitTimedOutInference(t, client, env.escrowURL(), executorSlot, executionTimeoutLandsWithin)
	record, ok := timedOut["record"].(map[string]any)
	require.True(t, ok, "timed-out inference should carry its record")
	require.NotZero(t, testutil.NumericField(t, record, "confirmed_at_time_ms"),
		"execution timeout should be anchored to the executor receipt")

	timeoutTx := awaitTimeoutTransaction(t, client, env, noSkippedSlot)
	testutil.RequireTimeoutInferenceTransaction(
		t, timeoutTx, testutil.NumericField(t, timedOut, "nonce"), types.TimeoutReason_TIMEOUT_REASON_EXECUTION,
		testutil.NumericField(t, escrowConfig(t, client, env.escrowURL()), "VoteThreshold"), executorSlot,
	)
	require.GreaterOrEqual(t, slotMisses(t, client, env.escrowURL(), executorSlot), uint64(1),
		"execution timeout should mark the stalled executor as missed")

	delete(env.hostEnvOverrides[executorSlot], stubExecutionHangEnv)
	env.restartHost(ctx, t, executorSlot)
	continued := testutil.SendCompletionRaw(t, client, env.clientURL, "execution timeout continued", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "execution timeout recovery completion", continued)
	testutil.RequireOpenAINonStreamingCompletion(t, continued)

	testutil.DriveUntilValidationObserved(t, client, env.clientURL, env.escrowURL())
	testutil.FinalizeSession(t, client, env.escrowURL())
	testutil.RequireFinalized(t, client, env.escrowURL())
}

// Test flow:
//  1. Start the three-host environment on the short-deadline chain config with every stub executor hanging after its receipt.
//  2. Send one completion, so no host is left to fail over to.
//  3. Assert the client sees 5xx within a minute, not a truncated 200 and not a header timeout.
func TestE2E_AllHostsExecutionHangSurfaces5xx(t *testing.T) {
	hang := map[string]string{stubExecutionHangEnv: "true"}
	opts := timeoutProtocolEnvOptions()
	opts.hostEnvOverrides = map[int]map[string]string{
		0: hang,
		1: hang,
		2: hang,
	}
	env, client := startGatewayEnv(t, opts)
	client.Timeout = allHostsHangAnswerWithin

	response := testutil.SendCompletionRaw(t, client, env.clientURL, "all hosts execution hang", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "all hosts execution hang", response)
	require.GreaterOrEqual(t, response.StatusCode, http.StatusInternalServerError,
		"with every slot hung the gateway must surface 5xx, not a truncated 200: %s", response.Body)
}
