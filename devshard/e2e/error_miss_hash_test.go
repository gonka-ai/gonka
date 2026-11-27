package e2e

import (
	"net/http"
	"testing"
	"time"

	"devshard/e2e/testutil"
	"devshard/internal/e2econfig"

	"github.com/stretchr/testify/require"
)

const missLandsWindow = 60 * time.Second

// startErrorMissEnv brings up a stand on short protocol deadlines, since a miss is claimed at its vote deadline, whose hosts refuse every inference after a role chunk.
func startErrorMissEnv(t *testing.T, logprobsOptimization string) (*e2eEnv, *http.Client) {
	t.Helper()
	hostEnv := shortDeadlines(map[string]string{e2econfig.StubInferenceErrorMissMessageEnv: "engine core refused after the role chunk"})
	if logprobsOptimization != "" {
		hostEnv["DEVSHARD_LOGPROBS_OPTIMIZATION_ENABLED"] = logprobsOptimization
	}
	return startGatewayEnv(t, e2eEnvOptions{
		hostEnv:             hostEnv,
		gatewayEnvOverrides: shortDeadlineGateway(),
		mockChainParams:     shortDeadlineChain(),
	})
}

// sendStreamingRefusal asks for a stream every host refuses, and returns whatever the client was shown.
func sendStreamingRefusal(t *testing.T, client *http.Client, clientURL, content string) testutil.RawResponse {
	t.Helper()
	refusal := testutil.PostJSONRaw(t, client, clientURL+"/v1/chat/completions", testutil.ChatCompletionBody(content, true), testutil.AdminAPIKey)
	testutil.LogRawResponse(t, "streaming refusal", refusal)
	return refusal
}

// Test flow:
//  1. Start the three-host environment on short protocol deadlines with the logprobs optimization on and every host refusing after a role chunk.
//  2. Send one streaming completion so the gateway holds a signed finish alongside the error envelope it received stripped.
//  3. Assert the client was shown the refusal, then poll the escrow state for a miss.
//  4. Assert a host was marked missed, because verifiers match the gateway's envelope against the signed served hash.
func TestE2E_ErrorMissLandsWhenTheExecutorForwardsTheServedView(t *testing.T) {
	env, client := startErrorMissEnv(t, "true")

	refusal := sendStreamingRefusal(t, client, env.clientURL, "error miss with the optimization on")

	require.Contains(t, refusal.Body, `"error"`, "the client was not shown the refusal: %d %s", refusal.StatusCode, refusal.Body)

	stats := testutil.WaitEscrowHostStats(t, client, env.escrowURL(), missLandsWindow, func(stats testutil.EscrowHostStats) bool {
		return stats.Missed > 0
	})
	require.Positive(t, stats.Missed, "the served hash makes the stripped error envelope provable, so the miss must land")
}

// Test flow:
//  1. Start the three-host environment on short protocol deadlines and the shipped defaults, with every host refusing after a role chunk.
//  2. Send one streaming completion so the gateway holds a signed finish alongside the error envelope.
//  3. Assert the client was shown the refusal, then poll the escrow state for a miss.
//  4. Assert a host was marked missed and the escrow pays only for inferences that did not miss, so the refusal is not paid for.
func TestE2E_ErrorMissLandsOnTheShippedDefaults(t *testing.T) {
	env, client := startErrorMissEnv(t, "")

	refusal := sendStreamingRefusal(t, client, env.clientURL, "error miss on the shipped defaults")

	require.Contains(t, refusal.Body, `"error"`, "the client was not shown the refusal: %d %s", refusal.StatusCode, refusal.Body)

	stats := testutil.WaitEscrowHostStats(t, client, env.escrowURL(), missLandsWindow, func(stats testutil.EscrowHostStats) bool {
		return stats.Missed > 0
	})
	require.Positive(t, stats.Missed, "a default-configured executor must forward a view the gateway can prove")
	require.Equal(t, testutil.CostOutsideMisses(t, client, env.escrowURL()), stats.Cost,
		"a missed inference is unwound, so the escrow pays only for inferences that did not miss")
}

// Test flow:
//  1. Start the three-host environment on short protocol deadlines with every host optimizing by its own default and refusing after a role chunk.
//  2. Tell the gateway through admin settings to ask executors for the bytes they stored.
//  3. Send one streaming completion and assert the client was shown the refusal.
//  4. Assert a host was marked missed, so the gateway's choice reached executors that were never restarted, and the escrow pays only for inferences that did not miss.
func TestE2E_GatewayChoiceMakesTheMissProvableWithoutRestartingHosts(t *testing.T) {
	env, client := startErrorMissEnv(t, "true")

	putSettings(t, client, env.clientURL, map[string]any{"logprobs_optimization": false})

	refusal := sendStreamingRefusal(t, client, env.clientURL, "error miss with the gateway overriding the hosts")
	require.Contains(t, refusal.Body, `"error"`, "the client was not shown the refusal: %d %s", refusal.StatusCode, refusal.Body)

	stats := testutil.WaitEscrowHostStats(t, client, env.escrowURL(), missLandsWindow, func(stats testutil.EscrowHostStats) bool {
		return stats.Missed > 0
	})
	require.Positive(t, stats.Missed, "the gateway's choice did not reach the executors")
	require.Equal(t, testutil.CostOutsideMisses(t, client, env.escrowURL()), stats.Cost,
		"a missed inference is unwound, so the escrow pays only for inferences that did not miss")
}
