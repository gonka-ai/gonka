package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
	"devshard/types"
)

// Test flow:
//  1. Start the default three-host environment and stop slot 1 before it can
//     process the first inference nonce.
//  2. Send a non-streaming completion through devshardctl and assert the
//     redundancy path still returns a valid response.
//  3. Wait for the escrow-bound refusal deadline and assert inference 1 becomes
//     timed out.
//  4. Read a live host's stored diffs and assert a MsgTimeoutInference was
//     applied with threshold-sufficient votes from the other slots.
//  5. Restart slot 1, continue the session, and finalize successfully.
func TestE2E_TimeoutProtocolEndToEnd(t *testing.T) {
	env, client := startNonStreamingEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), testutil.DefaultRequestTimeout)
	t.Cleanup(cancel)
	const unavailableSlot = 1
	const timedOutInferenceID = uint64(1)
	status := testutil.GetStatus(t, client, env.clientURL)
	config, ok := status["config"].(map[string]any)
	require.True(t, ok, "status config should be an object")
	require.Equal(t, uint64(5), testutil.NumericField(t, config, "refusal_timeout"),
		"timeout protocol should use the escrow-bound refusal timeout")
	env.stopHost(ctx, t, unavailableSlot)

	response := testutil.SendCompletionRaw(t, client, env.clientURL, "timeout protocol", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "timeout protocol fallback completion", response)
	testutil.RequireOpenAINonStreamingCompletion(t, response)

	var timedOut map[string]any
	timeoutApplied := assert.Eventually(t, func() bool {
		inference, found := testutil.InferenceStatus(t, client, env.clientURL, timedOutInferenceID)
		if !found || inference["status"] != "timed_out" {
			return false
		}
		timedOut = inference
		return true
	}, 25*time.Second, time.Second, "refused inference should become timed out")
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
//  2. Reduce only gateway response waiting so the stalled request is cancelled
//     while the protocol still uses the escrow-bound execution timeout.
//  3. Send a non-streaming completion and assert it fails after slot 1 returns
//     its executor receipt but does not complete execution.
//  4. Wait for the execution deadline and assert inference 1 becomes timed
//     out with TIMEOUT_REASON_EXECUTION and threshold-sufficient votes.
//  5. Assert the timeout transaction is persisted and the executor receives a
//     missed-host count.
//  6. Restart slot 1 without the hang, continue the session, and finalize
//     successfully.
func TestE2E_ExecutionTimeoutProtocolEndToEnd(t *testing.T) {
	requireE2EEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	const executorSlot = 1
	const timedOutInferenceID = uint64(1)
	env := startE2EEnv(ctx, t, requiredImages(t), e2eEnvOptions{
		hostVolumeNames: sqliteHostVolumeNames(t),
		hostEnvOverrides: map[int]map[string]string{
			executorSlot: {"DEVSHARD_STUB_EXECUTION_HANG": "true"},
		},
	})
	client := &http.Client{Timeout: testutil.DefaultRequestTimeout}

	status := testutil.GetStatus(t, client, env.clientURL)
	config, ok := status["config"].(map[string]any)
	require.True(t, ok, "status config should be an object")
	require.Equal(t, uint64(17), testutil.NumericField(t, config, "execution_timeout"),
		"timeout protocol should use the escrow-bound execution timeout")

	testutil.PostJSON(t, client, env.clientURL+"/v1/admin/settings", map[string]any{
		"redundancy": map[string]any{
			"non_stream_no_content_timeout_ms": 1000,
			"non_stream_max_attempt_wait_ms":   5000,
			"speed_policy":                     "legacy",
		},
	})

	response := testutil.SendCompletionRaw(t, client, env.clientURL, "execution timeout protocol", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "execution timeout stalled completion", response)
	require.GreaterOrEqual(t, response.StatusCode, 500, "stalled execution should fail the gateway request")

	var timedOut map[string]any
	timeoutApplied := assert.Eventually(t, func() bool {
		inference, found := testutil.InferenceStatus(t, client, env.clientURL, timedOutInferenceID)
		if !found || inference["status"] != "timed_out" {
			return false
		}
		timedOut = inference
		return true
	}, 30*time.Second, time.Second, "started inference should become execution timed out")
	require.True(t, timeoutApplied, "started inference should become execution timed out")
	require.Equal(t, uint64(executorSlot), testutil.NumericField(t, timedOut, "executor_slot"))
	require.NotZero(t, testutil.NumericField(t, timedOut, "confirmed_at"),
		"execution timeout should be anchored to the executor receipt")

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
