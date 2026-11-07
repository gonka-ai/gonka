package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
	"devshard/types"
)

// Test flow:
//  1. Start the default three-host environment and wait until slot 1 owns the
//     next traffic nonce, then stop that host before it can process the start.
//  2. Send a non-streaming completion through devshardctl and assert the
//     redundancy path still returns a valid response.
//  3. Wait for the escrow-bound refusal deadline and assert that traffic
//     inference becomes timed out.
//  4. Read a live host's stored diffs and assert a MsgTimeoutInference was
//     applied with threshold-sufficient votes from the other slots.
//  5. Restart slot 1, continue the session, and finalize successfully.
func TestE2E_TimeoutProtocolEndToEnd(t *testing.T) {
	requireE2EEnabled(t)
	for _, header := range []bool{false, true} {
		name := "legacy"
		if header {
			name = "header"
		}
		t.Run(name, func(t *testing.T) {
			runE2ETimeoutProtocol(t, header)
		})
	}
}

func runE2ETimeoutProtocol(t *testing.T, header bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	env := startE2EEnv(ctx, t, requiredImages(t), timeoutProtocolEnvOptions(header))
	client := &http.Client{Timeout: testutil.DefaultRequestTimeout}
	const unavailableSlot = 1
	status := testutil.GetStatus(t, client, env.clientURL)
	config, ok := status["config"].(map[string]any)
	require.True(t, ok, "status config should be an object")
	require.Equal(t, uint64(5), testutil.NumericField(t, config, "refusal_timeout"),
		"timeout protocol should use the escrow-bound refusal timeout")
	require.Equal(t, header, config["heightsync_timeout_clock"] == true,
		"session clock must match the escrow the mock chain seeded")
	requireHostTimeoutClock(t, client, env, header)
	waitNextTrafficSlot(t, client, env.clientURL, len(env.hostURLs), unavailableSlot)
	beforeNonce := testutil.LatestSessionNonce(t, client, env.clientURL)
	env.stopHost(ctx, t, unavailableSlot)

	response := testutil.SendCompletionRaw(t, client, env.clientURL, "timeout protocol", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "timeout protocol fallback completion", response)
	testutil.RequireOpenAINonStreamingCompletion(t, response)
	timedOutInferenceID := beforeNonce + 1
	t.Logf("refusal timeout target inference=%d before_nonce=%d latest_nonce=%d",
		timedOutInferenceID, beforeNonce, testutil.LatestSessionNonce(t, client, env.clientURL))

	var timedOut map[string]any
	timeoutApplied := assert.Eventually(t, func() bool {
		inference, found := testutil.InferenceStatus(t, client, env.clientURL, timedOutInferenceID)
		logTimeoutProbe(t, client, env.clientURL, timedOutInferenceID, inference, found)
		if !found || inference["status"] != "timed_out" {
			return false
		}
		timedOut = inference
		return true
	}, 90*time.Second, time.Second, "refused inference should become timed out")
	if !timeoutApplied {
		logTimeoutFailure(t, client, env, timedOutInferenceID)
	}
	require.True(t, timeoutApplied, "refused inference should become timed out")
	require.Equal(t, uint64(unavailableSlot), testutil.NumericField(t, timedOut, "executor_slot"))

	var timeoutTx testutil.TimeoutInferenceTransaction
	timeoutTransactionApplied := assert.Eventually(t, func() bool {
		latestNonce := testutil.LatestSessionNonce(t, client, env.clientURL)
		for slotID, hostURL := range env.hostControlURLs {
			if slotID == unavailableSlot {
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
	require.True(t, timeoutTransactionApplied, "host storage should include MsgTimeoutInference")
	state := testutil.GetJSON(t, client, env.clientURL+"/v1/state")
	session, ok := state["session"].(map[string]any)
	require.True(t, ok, "state session should be an object")
	config, ok = session["config"].(map[string]any)
	require.True(t, ok, "state session config should be an object")
	testutil.RequireTimeoutInferenceTransaction(
		t, timeoutTx, timedOutInferenceID, types.TimeoutReason_TIMEOUT_REASON_REFUSED,
		testutil.NumericField(t, config, "vote_threshold"), unavailableSlot,
	)

	hostStats, ok := state["host_stats"].(map[string]any)
	require.True(t, ok, "state host_stats should be an object")
	unavailableStats, ok := hostStats["1"].(map[string]any)
	require.True(t, ok, "state should include stats for unavailable slot")
	require.GreaterOrEqual(t, testutil.NumericField(t, unavailableStats, "missed"), uint64(1),
		"timeout should mark the unavailable executor as missed")

	env.hostControlURLs[unavailableSlot] = containerURL(ctx, t, env.startHost(ctx, t, unavailableSlot), "8080/tcp")
	continued := testutil.SendCompletionRaw(t, client, env.clientURL, "timeout protocol continued", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "timeout protocol continued completion", continued)
	testutil.RequireOpenAINonStreamingCompletion(t, continued)

	testutil.DriveUntilValidationObserved(t, client, env.clientURL)
	settlement := testutil.FinalizeSession(t, client, env.clientURL)
	testutil.RequireSettlementContract(t, settlement)
	testutil.RequireSettlementHostStats(t, settlement, len(testutil.HostPrivateKeys))
}

// Test flow:
//  1. Start the three-host environment with persistent SQLite volumes and the
//     slot 1 stub executor configured to hang after its receipt is accepted.
//  2. Wait until slot 1 owns the next traffic nonce, then shorten the
//     post-winner speculative wait so the hanging primary is cancelled quickly
//     and HandleTimeout can start. Always-stream first-token escalation still
//     fails over to another host, so the client gets a completion while that
//     traffic nonce remains started-without-Finish on slot 1.
//  3. Wait for the escrow-bound execution deadline and assert that traffic
//     inference becomes timed out with TIMEOUT_REASON_EXECUTION and
//     threshold-sufficient votes.
//  4. Assert the timeout transaction is persisted and the executor receives a
//     missed-host count.
//  5. Restart slot 1 without the hang env, continue the session, and finalize.
func TestE2E_ExecutionTimeoutProtocolEndToEnd(t *testing.T) {
	requireE2EEnabled(t)
	for _, header := range []bool{false, true} {
		name := "legacy"
		if header {
			name = "header"
		}
		t.Run(name, func(t *testing.T) {
			runE2EExecutionTimeoutProtocol(t, header)
		})
	}
}

func runE2EExecutionTimeoutProtocol(t *testing.T, header bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	const executorSlot = 1
	opts := timeoutProtocolEnvOptions(header)
	opts.hostVolumeNames = sqliteHostVolumeNames(t)
	opts.hostEnvOverrides = map[int]map[string]string{
		executorSlot: {"DEVSHARD_STUB_EXECUTION_HANG": "true"},
	}
	env := startE2EEnv(ctx, t, requiredImages(t), opts)
	client := &http.Client{Timeout: testutil.DefaultRequestTimeout}

	status := testutil.GetStatus(t, client, env.clientURL)
	config, ok := status["config"].(map[string]any)
	require.True(t, ok, "status config should be an object")
	require.Equal(t, uint64(17), testutil.NumericField(t, config, "execution_timeout"),
		"timeout protocol should use the escrow-bound execution timeout")
	require.Equal(t, header, config["heightsync_timeout_clock"] == true,
		"session clock must match the escrow the mock chain seeded")
	requireHostTimeoutClock(t, client, env, header)

	waitNextTrafficSlot(t, client, env.clientURL, len(env.hostURLs), executorSlot)
	beforeNonce := testutil.LatestSessionNonce(t, client, env.clientURL)
	restoreSecondaryWait := overrideSecondaryWaitAfterWinnerMS(t, client, env.clientURL, 500)

	response := testutil.SendCompletionRaw(t, client, env.clientURL, "execution timeout protocol", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "execution timeout failover completion", response)
	testutil.RequireOpenAINonStreamingCompletion(t, response)
	timedOutInferenceID := beforeNonce + 1
	t.Logf("execution timeout target inference=%d before_nonce=%d latest_nonce=%d",
		timedOutInferenceID, beforeNonce, testutil.LatestSessionNonce(t, client, env.clientURL))

	var timedOut map[string]any
	timeoutApplied := assert.Eventually(t, func() bool {
		inference, found := testutil.InferenceStatus(t, client, env.clientURL, timedOutInferenceID)
		logTimeoutProbe(t, client, env.clientURL, timedOutInferenceID, inference, found)
		if !found || inference["status"] != "timed_out" {
			return false
		}
		timedOut = inference
		return true
	}, 40*time.Second, time.Second, "started inference should become execution timed out")
	if !timeoutApplied {
		logTimeoutFailure(t, client, env, timedOutInferenceID)
	}
	require.True(t, timeoutApplied, "started inference should become execution timed out")
	require.Equal(t, uint64(executorSlot), testutil.NumericField(t, timedOut, "executor_slot"))
	require.NotZero(t, testutil.NumericField(t, timedOut, "confirmed_at_height"),
		"execution timeout should be anchored to the executor receipt height")

	var timeoutTx testutil.TimeoutInferenceTransaction
	timeoutTransactionApplied := assert.Eventually(t, func() bool {
		latestNonce := testutil.LatestSessionNonce(t, client, env.clientURL)
		for _, hostURL := range env.hostControlURLs {
			transaction, found := testutil.FindTimeoutInferenceTransaction(
				t, client, hostURL, e2eHostRoutePrefix, defaultEscrowID, latestNonce,
			)
			if found {
				timeoutTx = transaction
				return true
			}
		}
		return false
	}, 10*time.Second, 250*time.Millisecond, "host storage should include execution MsgTimeoutInference")
	require.True(t, timeoutTransactionApplied, "host storage should include execution MsgTimeoutInference")

	state := testutil.GetJSON(t, client, env.clientURL+"/v1/state")
	session, ok := state["session"].(map[string]any)
	require.True(t, ok, "state session should be an object")
	config, ok = session["config"].(map[string]any)
	require.True(t, ok, "state session config should be an object")
	testutil.RequireTimeoutInferenceTransaction(
		t, timeoutTx, timedOutInferenceID, types.TimeoutReason_TIMEOUT_REASON_EXECUTION,
		testutil.NumericField(t, config, "vote_threshold"), executorSlot,
	)

	hostStats, ok := state["host_stats"].(map[string]any)
	require.True(t, ok, "state host_stats should be an object")
	executorStats, ok := hostStats["1"].(map[string]any)
	require.True(t, ok, "state should include stats for the executor slot")
	require.GreaterOrEqual(t, testutil.NumericField(t, executorStats, "missed"), uint64(1),
		"execution timeout should mark the stalled executor as missed")

	restoreSecondaryWait()
	// restartHost rebuilds from hostEnvOverrides; keep the hang off so recovery
	// completions finish instead of stacking started-without-Finish inferences.
	delete(env.hostEnvOverrides[executorSlot], "DEVSHARD_STUB_EXECUTION_HANG")
	env.restartHost(ctx, t, executorSlot)
	testutil.PostJSON(t, client, env.clientURL+"/v1/debug/sync-hosts", map[string]any{})
	continued := testutil.SendCompletionRaw(t, client, env.clientURL, "execution timeout continued", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "execution timeout recovery completion", continued)
	testutil.RequireOpenAINonStreamingCompletion(t, continued)

	testutil.DriveUntilValidationObserved(t, client, env.clientURL)
	settlement := testutil.FinalizeSession(t, client, env.clientURL)
	testutil.RequireSettlementContract(t, settlement)
	testutil.RequireSettlementHostStats(t, settlement, len(testutil.HostPrivateKeys))
}

func timeoutProtocolEnvOptions(header bool) e2eEnvOptions {
	mockChainEnv := map[string]string{
		"MOCK_CHAIN_CONFIG": "/app/mock-chain-timeout-config.yaml",
	}
	hostEnv := map[string]string{
		"DEVSHARD_REFUSAL_TIMEOUT":   "5",
		"DEVSHARD_EXECUTION_TIMEOUT": "17",
	}
	if header {
		// The mock chain writes this onto params and the seed escrow. The
		// gateway binds that escrow. devshard-host binds the same value from
		// DEVSHARD_HEIGHTSYNC_TIMEOUT_CLOCK. Unset on the host is legacy.
		mockChainEnv["MOCK_CHAIN_HEIGHTSYNC_TIMEOUT_CLOCK"] = "true"
		hostEnv["DEVSHARD_HEIGHTSYNC_TIMEOUT_CLOCK"] = "true"
	}
	return e2eEnvOptions{
		hostEnv:      hostEnv,
		mockChainEnv: mockChainEnv,
	}
}

func requireHostTimeoutClock(t *testing.T, client *http.Client, env *e2eEnv, header bool) {
	t.Helper()
	for slotID, hostURL := range env.hostControlURLs {
		body := testutil.GetJSON(t, client, hostURL+e2eHostRoutePrefix+"/debug/timeout-clock")
		require.Equal(t, header, body["heightsync_timeout_clock"] == true,
			"host slot %d clock must match the escrow", slotID)
	}
}

// Test flow:
//  1. Hang every stub executor after receipt so always-stream has no failover host.
//  2. First-token escalation starts every remaining host; with none left it
//     fail-closes instead of waiting out meta-drain or the protocol execution timeout.
//  3. Assert the client sees 5xx, not a truncated 200 and not a header timeout.
func TestE2E_AllHostsExecutionHangSurfaces5xx(t *testing.T) {
	requireE2EEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	hang := map[string]string{"DEVSHARD_STUB_EXECUTION_HANG": "true"}
	opts := timeoutProtocolEnvOptions(false)
	opts.hostEnvOverrides = map[int]map[string]string{
		0: hang,
		1: hang,
		2: hang,
	}
	env := startE2EEnv(ctx, t, requiredImages(t), opts)
	client := &http.Client{Timeout: 15 * time.Second}

	response := testutil.SendCompletionRaw(t, client, env.clientURL, "all hosts execution hang", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "all hosts execution hang", response)
	require.GreaterOrEqual(t, response.StatusCode, http.StatusInternalServerError,
		"with every slot hung the gateway must surface 5xx, not a truncated 200: %s", response.Body)
}

func overrideSecondaryWaitAfterWinnerMS(t *testing.T, client *http.Client, clientURL string, ms int64) func() {
	t.Helper()
	settings := testutil.GetJSON(t, client, clientURL+"/v1/admin/settings")
	redundancy, ok := settings["redundancy"].(map[string]any)
	require.True(t, ok, "admin settings redundancy should be an object")
	prev := testutil.NumericField(t, redundancy, "secondary_wait_after_winner_ms")
	testutil.PostJSON(t, client, clientURL+"/v1/admin/settings", map[string]any{
		"redundancy": map[string]any{
			"secondary_wait_after_winner_ms": ms,
		},
	})
	var restored bool
	restore := func() {
		if restored {
			return
		}
		restored = true
		testutil.PostJSON(t, client, clientURL+"/v1/admin/settings", map[string]any{
			"redundancy": map[string]any{
				"secondary_wait_after_winner_ms": int64(prev),
			},
		})
	}
	t.Cleanup(restore)
	return restore
}

func logTimeoutProbe(t *testing.T, client *http.Client, clientURL string, inferenceID uint64, inference map[string]any, found bool) {
	t.Helper()
	latest := testutil.LatestSessionNonce(t, client, clientURL)
	if !found {
		t.Logf("timeout probe inference=%d found=false latest_nonce=%d", inferenceID, latest)
		return
	}
	t.Logf("timeout probe inference=%d status=%v executor_slot=%v started_at=%v started_at_time_ms=%v confirmed_at=%v confirmed_at_time_ms=%v latest_nonce=%d",
		inferenceID,
		inference["status"],
		inference["executor_slot"],
		inference["started_at"],
		inference["started_at_time_ms"],
		inference["confirmed_at"],
		inference["confirmed_at_time_ms"],
		latest,
	)
}

func logTimeoutFailure(t *testing.T, client *http.Client, env *e2eEnv, inferenceID uint64) {
	t.Helper()
	inferences := testutil.GetJSON(t, client, env.clientURL+"/v1/debug/inferences")
	if raw, err := json.MarshalIndent(inferences, "", "  "); err == nil {
		t.Logf("timeout failure inferences dump:\n%s", raw)
	} else {
		t.Logf("timeout failure inferences dump: %#v", inferences)
	}
	heightSync := testutil.GetJSON(t, client, env.clientURL+"/v1/debug/heightsync")
	if raw, err := json.MarshalIndent(heightSync, "", "  "); err == nil {
		t.Logf("timeout failure heightsync dump:\n%s", raw)
	}
	state := testutil.GetJSON(t, client, env.clientURL+"/v1/state")
	t.Logf("timeout failure latest_nonce=%d target_inference=%d",
		testutil.LatestSessionNonce(t, client, env.clientURL), inferenceID)
	if session, ok := state["session"].(map[string]any); ok {
		t.Logf("timeout failure session phase=%v floor=%v", session["phase"], session["height_sync_floor"])
	}
	for slotID, hostURL := range env.hostControlURLs {
		t.Logf("timeout failure host slot=%d control=%s", slotID, hostURL)
	}
}
