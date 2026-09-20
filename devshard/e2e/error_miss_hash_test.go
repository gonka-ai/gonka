package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/e2e/testutil"
	"devshard/internal/e2econfig"

	"github.com/stretchr/testify/require"
)

const (
	missLandsWindow      = 60 * time.Second
	missNeverLandsWindow = 20 * time.Second
)

// startErrorMissEnv brings up a stand whose hosts refuse every inference after a role chunk.
func startErrorMissEnv(t *testing.T, logprobsOptimization string) (*e2eEnv, *http.Client) {
	t.Helper()
	hostEnv := map[string]string{e2econfig.StubInferenceErrorMissMessageEnv: "engine core refused after the role chunk"}
	if logprobsOptimization != "" {
		hostEnv["DEVSHARD_LOGPROBS_OPTIMIZATION_ENABLED"] = logprobsOptimization
	}
	return startNonStreamingEnvWithOptions(t, e2eEnvOptions{hostEnv: hostEnv})
}

// Test flow:
//  1. Start the three-host environment with the logprobs optimization on and every host answering content then a terminal error.
//  2. Send one streaming completion so the gateway holds a signed finish alongside the error envelope.
//  3. Assert the client was shown the refusal, then poll the escrow state for a miss.
//  4. Assert no host was ever marked missed, because verifiers cannot rehash what the gateway saw.
func TestE2E_ErrorMissRejectedWhenTheExecutorSlimsWhatItStores(t *testing.T) {
	env, client := startErrorMissEnv(t, "true")
	nonceBefore := testutil.LatestSessionNonce(t, client, env.clientURL)

	stream := testutil.SendStreamingCompletion(t, client, env.clientURL, "error miss with the optimization on")
	testutil.DebugLogf(t, "client stream: %v", stream.Events)

	require.Contains(t, strings.Join(stream.Events, " "), `"error"`, "the client was not shown the refusal: %v", stream.Events)

	stats := testutil.WaitEscrowHostStats(t, client, env.clientURL, missNeverLandsWindow, func(stats testutil.EscrowHostStats) bool {
		return stats.Missed > 0
	})
	require.Greater(t, testutil.LatestSessionNonce(t, client, env.clientURL), nonceBefore,
		"the stand never spent a nonce, so the absent miss proves nothing")
	require.Zero(t, stats.Missed, "a slimmed stored copy leaves the gateway's proof unhashable, so no miss can land")
}

// Test flow:
//  1. Start the three-host environment on the shipped defaults, with every host refusing after a role chunk.
//  2. Send one streaming completion so the gateway holds a signed finish alongside the error envelope.
//  3. Assert the client was shown the refusal, then poll the escrow state for a miss.
//  4. Assert a host was marked missed and the refusal was unwound, so it is not paid for.
func TestE2E_ErrorMissLandsOnTheShippedDefaults(t *testing.T) {
	env, client := startErrorMissEnv(t, "")

	stream := testutil.SendStreamingCompletion(t, client, env.clientURL, "error miss on the shipped defaults")
	testutil.DebugLogf(t, "client stream: %v", stream.Events)

	require.Contains(t, strings.Join(stream.Events, " "), `"error"`, "the client was not shown the refusal: %v", stream.Events)

	stats := testutil.WaitEscrowHostStats(t, client, env.clientURL, missLandsWindow, func(stats testutil.EscrowHostStats) bool {
		return stats.Missed > 0
	})
	require.Positive(t, stats.Missed, "a default-configured executor must forward the bytes it hashed")
	require.Zero(t, stats.Cost, "a missed inference is unwound, so the refusal is not paid for")
}

// Test flow:
//  1. Start the three-host environment with every host optimizing by its own default and refusing after a role chunk.
//  2. Tell the gateway through admin settings to ask executors for the bytes they stored.
//  3. Send one streaming completion and assert the client was shown the refusal.
//  4. Assert a host was marked missed, so the gateway's choice reached executors that were never restarted.
func TestE2E_GatewayChoiceMakesTheMissProvableWithoutRestartingHosts(t *testing.T) {
	env, client := startErrorMissEnv(t, "true")

	testutil.PostJSON(t, client, env.clientURL+"/v1/admin/settings", map[string]any{
		"logprobs_optimization": map[string]any{"enabled": false},
	})

	stream := testutil.SendStreamingCompletion(t, client, env.clientURL, "error miss with the gateway overriding the hosts")
	require.Contains(t, strings.Join(stream.Events, " "), `"error"`, "the client was not shown the refusal: %v", stream.Events)

	stats := testutil.WaitEscrowHostStats(t, client, env.clientURL, missLandsWindow, func(stats testutil.EscrowHostStats) bool {
		return stats.Missed > 0
	})
	require.Positive(t, stats.Missed, "the gateway's choice did not reach the executors")
	require.Zero(t, stats.Cost, "a missed inference is unwound, so the refusal is not paid for")
}
