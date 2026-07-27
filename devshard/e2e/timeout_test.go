package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
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
		t, timeoutTx, timedOutInferenceID, testutil.NumericField(t, config, "vote_threshold"), unavailableSlot,
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
