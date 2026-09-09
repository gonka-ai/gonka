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
//  3. Send a non-streaming chat completion through devshardctl.
//  4. Assert the gateway still returns valid OpenAI-compatible JSON.
//  5. Assert protocol/internal metadata does not leak into the response.
func TestE2E_NonStreamingOneHostUnavailable(t *testing.T) {
	env, client := startNonStreamingEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), testutil.DefaultRequestTimeout)
	t.Cleanup(cancel)
	env.stopHost(ctx, t, 1)

	resp := testutil.SendCompletionRaw(t, client, env.clientURL, "non-streaming one host unavailable", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "one host unavailable completion", resp)
	testutil.RequireOpenAINonStreamingCompletion(t, resp)
}

// Test flow:
//  1. Start the default three-host environment.
//  2. Stop one devshard-host container.
//  3. Send a non-streaming completion and assert success.
//  4. Start the same devshard-host again.
//  5. Send another non-streaming completion and assert success.
//  6. Assert the session nonce continues to progress after recovery.
func TestE2E_NonStreamingHostUnavailableThenRecovers(t *testing.T) {
	env, client := startNonStreamingEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), testutil.DefaultRequestTimeout)
	t.Cleanup(cancel)
	env.stopHost(ctx, t, 1)

	downResp := testutil.SendCompletionRaw(t, client, env.clientURL, "non-streaming host down", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "host down completion", downResp)
	testutil.RequireOpenAINonStreamingCompletion(t, downResp)
	beforeRecovery := testutil.LatestSessionNonce(t, client, env.clientURL)

	env.startHost(ctx, t, 1)

	recoveredResp := testutil.SendCompletionRaw(t, client, env.clientURL, "non-streaming host recovered", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "host recovered completion", recoveredResp)
	testutil.RequireOpenAINonStreamingCompletion(t, recoveredResp)
	afterRecovery := testutil.LatestSessionNonce(t, client, env.clientURL)
	require.Greater(t, afterRecovery, beforeRecovery, "session nonce should progress after host recovery")
}

// Test flow:
//  1. POST malformed JSON to /v1/chat/completions.
//  2. Assert devshardctl rejects the request with a client error.
//  3. Assert the gateway does not return a 5xx response.
func TestE2E_NonStreamingRejectsMalformedJSON(t *testing.T) {
	env, client := startNonStreamingEnv(t)

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
//  1. Send non-streaming completions through devshardctl.
//  2. Wait until validation evidence is visible in /v1/state.
//  3. Finalize the session.
//  4. Send another non-streaming completion.
//  5. Assert the finalized session rejects new inference requests.
func TestE2E_NonStreamingRejectsAfterFinalize(t *testing.T) {
	env, client := startNonStreamingEnv(t)

	testutil.SendCompletions(t, client, env.clientURL, "non-streaming before finalize", 3)
	testutil.DriveUntilValidationObserved(t, client, env.clientURL)
	settlement := testutil.FinalizeSession(t, client, env.clientURL)
	testutil.RequireSettlementContract(t, settlement)

	resp := testutil.SendCompletionRaw(t, client, env.clientURL, "non-streaming after finalize", testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "post-finalize completion", resp)
	require.Equal(t, http.StatusBadGateway, resp.StatusCode, "post-finalize inference should be rejected because no runtime is available: %s", resp.Body)
}
