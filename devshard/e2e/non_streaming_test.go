package e2e

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
)

// Test flow:
//  1. Send three OpenAI-style chat completion requests through the gateway.
//  2. Drive extra completion rounds until one slot has completed two validations.
//  3. Finalize the escrow through the gateway.
//  4. Assert the escrow left the active phase at a finalize nonce its slots reached signature quorum on.
//  5. Assert the escrow state carries host stats for every host.
func TestE2E_NonStreamingHappyPath(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})
	testutil.SendCompletions(t, client, env.clientURL, "hello", 3)

	testutil.DriveUntilValidationObserved(t, client, env.clientURL, env.escrowURL())

	testutil.FinalizeSession(t, client, env.escrowURL())
	testutil.RequireFinalized(t, client, env.escrowURL())
	require.Len(t, testutil.EscrowHostStatsBySlot(t, client, env.escrowURL()), e2eHostCount,
		"the finalized escrow should carry host stats for every host")
}

// Test flow:
//  1. Send non-streaming prompt A through the gateway.
//  2. Send non-streaming prompt B through the gateway.
//  3. Send prompt A again to exercise cache lookup.
//  4. Assert all responses remain valid OpenAI-compatible JSON.
//  5. Assert no response is replayed as SSE or protocol metadata.
func TestE2E_NonStreamingDifferentPromptsDoNotCollide(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	for _, content := range []string{
		"non-streaming cache prompt A",
		"non-streaming cache prompt B",
		"non-streaming cache prompt A",
	} {
		resp := testutil.SendCompletionRaw(t, client, env.clientURL, content, testutil.AdminAPIKey)
		testutil.RequireOpenAINonStreamingCompletion(t, resp)
	}
}

// Test flow:
//  1. Send a non-streaming completion without Authorization.
//  2. Assert the request is rejected.
//  3. Send the same request with an invalid bearer token.
//  4. Assert the request is rejected.
//  5. Send the request with the configured admin token and assert success.
func TestE2E_NonStreamingRequiresAuth(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	noAuth := testutil.SendCompletionRaw(t, client, env.clientURL, "non-streaming no auth", "")
	require.Equal(t, http.StatusUnauthorized, noAuth.StatusCode, "missing auth should be rejected: %s", noAuth.Body)

	wrongAuth := testutil.SendCompletionRaw(t, client, env.clientURL, "non-streaming wrong auth", "wrong-token")
	require.Equal(t, http.StatusUnauthorized, wrongAuth.StatusCode, "invalid auth should be rejected: %s", wrongAuth.Body)

	authorized := testutil.SendCompletionRaw(t, client, env.clientURL, "non-streaming valid auth", testutil.AdminAPIKey)
	testutil.RequireOpenAINonStreamingCompletion(t, authorized)
}

// Test flow:
//  1. Send malformed non-streaming completion requests through the gateway.
//  2. Assert each request is rejected with a client error.
//  3. Assert each rejection returns a JSON error body.
//  4. Assert invalid requests do not crash the gateway with a 5xx response.
func TestE2E_NonStreamingRejectsInvalidRequest(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	cases := []struct {
		name string
		body map[string]any
	}{
		{
			name: "missing messages",
			body: map[string]any{"model": defaultStandModel},
		},
		{
			name: "unknown model",
			body: map[string]any{
				"model": "missing-model",
				"messages": []map[string]string{
					{"role": "user", "content": "hello"},
				},
			},
		},
		{
			name: "invalid messages shape",
			body: map[string]any{
				"model":    defaultStandModel,
				"messages": "not-an-array",
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resp := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", testCase.body, testutil.AdminAPIKey)
			require.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest, "invalid request should be rejected: %s", resp.Body)
			require.Less(t, resp.StatusCode, http.StatusInternalServerError, "invalid request should not crash gateway: %s", resp.Body)
			require.NotNil(t, resp.JSON, "invalid request should return JSON error: %s", resp.Body)
			require.Contains(t, fmt.Sprint(resp.JSON["error"]), "message", "error response should include a message")
		})
	}
}
