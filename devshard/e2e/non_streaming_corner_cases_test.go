package e2e

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
)

// Test flow:
//  1. Start the default three-host environment.
//  2. Stop one devshard-host container.
//  3. Send a non-streaming completion and assert success.
//  4. Start the same devshard-host again.
//  5. Send another non-streaming completion and assert success.
//  6. Assert the session nonce continues to progress after recovery.
func TestE2E_NonStreamingHostUnavailableThenRecovers(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), testutil.DefaultRequestTimeout)
	t.Cleanup(cancel)
	env.stopHost(ctx, t, 1)

	downResp := testutil.SendCompletionRaw(t, client, env.clientURL, "non-streaming host down", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "host down completion", downResp)
	testutil.RequireOpenAINonStreamingCompletion(t, downResp)
	beforeRecovery := gatewaySessionNonce(t, client, env.clientURL)

	env.startHost(ctx, t, 1)

	recoveredResp := testutil.SendCompletionRaw(t, client, env.clientURL, "non-streaming host recovered", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "host recovered completion", recoveredResp)
	testutil.RequireOpenAINonStreamingCompletion(t, recoveredResp)
	afterRecovery := gatewaySessionNonce(t, client, env.clientURL)
	require.Greater(t, afterRecovery, beforeRecovery, "session nonce should progress after host recovery")
}

// Test flow:
//  1. POST malformed JSON to /v1/chat/completions.
//  2. Assert the gateway rejects the request with a client error.
//  3. Assert the gateway does not return a 5xx response.
func TestE2E_NonStreamingRejectsMalformedJSON(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	resp := testutil.PostRaw(
		t,
		client,
		env.clientURL+"/v1/chat/completions",
		"application/json",
		[]byte(`{"model":"stub-model","messages":[`),
		testutil.AdminAPIKey,
	)
	testutil.LogRawResponse(t, "malformed JSON completion", resp)
	require.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest, "malformed JSON should be rejected: %s", resp.Body)
	require.Less(t, resp.StatusCode, http.StatusInternalServerError, "malformed JSON should not crash gateway: %s", resp.Body)
}

// Test flow:
//  1. Send non-streaming completions through the gateway.
//  2. Drive extra completions until one slot has completed two validations.
//  3. Finalize the escrow and assert it left the active phase.
//  4. Send another non-streaming completion.
//  5. Assert the gateway answers 503 naming the model with no routable devshard: the finalized escrow serves no new inference request.
func TestE2E_NonStreamingRejectsAfterFinalize(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	testutil.SendCompletions(t, client, env.clientURL, "non-streaming before finalize", 3)
	testutil.DriveUntilValidationObserved(t, client, env.clientURL, env.escrowURL())
	testutil.FinalizeSession(t, client, env.escrowURL())
	testutil.RequireFinalized(t, client, env.escrowURL())

	resp := testutil.SendCompletionRaw(t, client, env.clientURL, "non-streaming after finalize", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "post-finalize completion", resp)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "post-finalize inference should be refused: %s", resp.Body)
	require.JSONEq(t, `{"error":{"message":"model \"stub-model\" is temporarily unavailable: no devshard is currently routable"}}`, resp.Body)
}
