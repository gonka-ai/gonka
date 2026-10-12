package e2e

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
)

// Test flow:
//  1. Send three OpenAI-style chat completion requests through devshardctl.
//  2. Assert that every completion response includes choices.
//  3. Drive extra completion rounds until one host reaches at least two
//     completed validations.
//  4. Finalize the devshard session through devshardctl.
//  5. Verify the stable settlement contract:
//     escrow id, version, state root, nonce, signatures, and no duplicate
//     signature slot ids, with one host_stats entry per host.
func TestE2E_NonStreamingHappyPath(t *testing.T) {
	env, client := startNonStreamingEnv(t)
	testutil.SendCompletions(t, client, env.clientURL, "hello", 3)

	testutil.DriveUntilValidationObserved(t, client, env.clientURL)

	settlement := testutil.FinalizeSession(t, client, env.clientURL)
	testutil.RequireSettlementContract(t, settlement)
	testutil.RequireSettlementHostStats(t, settlement, len(testutil.HostPrivateKeys))
}

// Test flow:
//  1. Send one non-streaming OpenAI-style chat completion through devshardctl.
//  2. Assert the response is JSON, not SSE.
//  3. Assert every choice uses the non-streaming message shape.
//  4. Assert devshard protocol metadata does not leak into the response body.
func TestE2E_NonStreamingChatCompletionShape(t *testing.T) {
	env, client := startNonStreamingEnv(t)

	resp := testutil.SendCompletionRaw(t, client, env.clientURL, "non-streaming shape", testutil.AdminAPIKey)
	testutil.RequireOpenAINonStreamingCompletion(t, resp)
}

// Test flow:
//  1. Send a non-streaming completion through devshardctl.
//  2. Send the same non-streaming completion again.
//  3. Assert both responses keep valid OpenAI-compatible JSON shape.
//  4. Assert the possible cached response is not replayed as SSE or protocol
//     metadata.
func TestE2E_NonStreamingCacheHitKeepsJSONShape(t *testing.T) {
	env, client := startNonStreamingEnv(t)

	content := "non-streaming cache shape"
	first := testutil.SendCompletionRaw(t, client, env.clientURL, content, testutil.AdminAPIKey)
	testutil.RequireOpenAINonStreamingCompletion(t, first)

	second := testutil.SendCompletionRaw(t, client, env.clientURL, content, testutil.AdminAPIKey)
	testutil.RequireOpenAINonStreamingCompletion(t, second)
}

// Test flow:
//  1. Send non-streaming prompt A through devshardctl.
//  2. Send non-streaming prompt B through devshardctl.
//  3. Send prompt A again to exercise cache lookup.
//  4. Assert all responses remain valid OpenAI-compatible JSON.
//  5. Assert no response is replayed as SSE or protocol metadata.
func TestE2E_NonStreamingDifferentPromptsDoNotCollide(t *testing.T) {
	env, client := startNonStreamingEnv(t)

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
	env, client := startNonStreamingEnv(t)

	noAuth := testutil.SendCompletionRaw(t, client, env.clientURL, "non-streaming no auth", "")
	require.Equal(t, http.StatusUnauthorized, noAuth.StatusCode, "missing auth should be rejected: %s", noAuth.Body)

	wrongAuth := testutil.SendCompletionRaw(t, client, env.clientURL, "non-streaming wrong auth", "wrong-token")
	require.Equal(t, http.StatusUnauthorized, wrongAuth.StatusCode, "invalid auth should be rejected: %s", wrongAuth.Body)

	ok := testutil.SendCompletionRaw(t, client, env.clientURL, "non-streaming valid auth", testutil.AdminAPIKey)
	testutil.RequireOpenAINonStreamingCompletion(t, ok)
}

// Test flow:
//  1. Send malformed non-streaming completion requests through devshardctl.
//  2. Assert each request is rejected with a client error.
//  3. Assert each rejection returns a JSON error body.
//  4. Assert invalid requests do not crash the gateway with a 5xx response.
func TestE2E_NonStreamingRejectsInvalidRequest(t *testing.T) {
	env, client := startNonStreamingEnv(t)

	cases := []struct {
		name string
		body map[string]any
	}{
		{
			name: "missing messages",
			body: map[string]any{"model": "stub-model"},
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
				"model":    "stub-model",
				"messages": "not-an-array",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := testutil.PostJSONRaw(t, client, env.clientURL+"/v1/chat/completions", tc.body, testutil.AdminAPIKey)
			require.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest, "invalid request should be rejected: %s", resp.Body)
			require.Less(t, resp.StatusCode, http.StatusInternalServerError, "invalid request should not crash gateway: %s", resp.Body)
			require.NotNil(t, resp.JSON, "invalid request should return JSON error: %s", resp.Body)
			require.Contains(t, fmt.Sprint(resp.JSON["error"]), "message", "error response should include a message")
		})
	}
}

func startNonStreamingEnv(t *testing.T) (*e2eEnv, *http.Client) {
	t.Helper()
	return startNonStreamingEnvWithOptions(t, e2eEnvOptions{})
}

func startNonStreamingEnvWithOptions(t *testing.T, opts e2eEnvOptions) (*e2eEnv, *http.Client) {
	t.Helper()
	requireE2EEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	images := requiredImages(t)
	env := startE2EEnv(ctx, t, images, opts)
	client := &http.Client{Timeout: testutil.DefaultRequestTimeout}
	return env, client
}
