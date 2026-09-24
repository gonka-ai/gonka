package e2e

import (
	"net/http"
	"testing"
	"time"

	"devshard/e2e/testutil"
	"devshard/internal/e2econfig"

	"github.com/stretchr/testify/require"
)

const servedBindingWindow = 30 * time.Second

// requireEveryStreamBound waits for the gateway to check both streams and asserts each matched a hash its executor signed.
func requireEveryStreamBound(t *testing.T, client *http.Client, clientURL string) {
	t.Helper()
	bindings := testutil.WaitServedBindings(t, client, clientURL, servedBindingWindow, func(bindings testutil.ServedBindings) bool {
		return bindings["bound"] >= 2
	})
	require.GreaterOrEqual(t, bindings["bound"], float64(2), "both streams must match a signed hash: %v", bindings)
	require.Zero(t, bindings["mismatch"], "an honest executor's stream was not bound: %v", bindings)
	require.Zero(t, bindings["missing"], "an honest executor signed no served hash: %v", bindings)
}

// startProcessedStreamEnv brings up a stand whose hosts answer through the executor's own processor.
func startProcessedStreamEnv(t *testing.T, logprobsOptimization string) (*e2eEnv, *http.Client) {
	t.Helper()
	hostEnv := map[string]string{e2econfig.StubInferenceProcessedStreamEnv: "true"}
	if logprobsOptimization != "" {
		hostEnv["DEVSHARD_LOGPROBS_OPTIMIZATION_ENABLED"] = logprobsOptimization
	}
	return startNonStreamingEnvWithOptions(t, e2eEnvOptions{hostEnv: hostEnv})
}

// Test flow:
//  1. Start the three-host environment with the logprobs optimization on and hosts answering through the executor's processor.
//  2. Send one streaming completion that does not ask for logprobs.
//  3. Send one streaming completion that asks for logprobs with one alternative.
//  4. Assert the silent client is shown no logprobs and the asking client is shown the host's own positions.
//  5. Assert neither client is shown the serving engine's bookkeeping.
//  6. Assert the gateway bound both streams to their signed Finish: the silent one by served_hash, the asking one by response_hash.
func TestE2E_LogprobsReachOnlyAskingClientsWithTheOptimizationOn(t *testing.T) {
	env, client := startProcessedStreamEnv(t, "true")

	silent := testutil.SendStreamingCompletion(t, client, env.clientURL, "logprobs not asked, optimization on")
	asking := testutil.SendStreamingCompletionWithLogprobs(t, client, env.clientURL, "logprobs asked, optimization on", 1)

	require.False(t, testutil.StreamCarries(silent, `"logprobs"`), "a client that did not ask was sent logprobs: %v", silent.Events)
	require.True(t, testutil.StreamCarries(asking, `"bytes"`), "an asking client must get the host's own positions: %v", asking.Events)
	require.True(t, testutil.StreamCarries(asking, `"top_logprobs"`), "an asking client must get the alternatives: %v", asking.Events)
	testutil.RequireNoEngineBookkeeping(t, silent)
	testutil.RequireNoEngineBookkeeping(t, asking)
	requireEveryStreamBound(t, client, env.clientURL)
}

// Test flow:
//  1. Start the three-host environment with the logprobs optimization off and hosts answering through the executor's processor.
//  2. Send one streaming completion that does not ask for logprobs.
//  3. Send one streaming completion that asks for logprobs with one alternative.
//  4. Assert the executor now forwards logprobs for every request yet the gateway still withholds them from the silent client.
//  5. Assert the asking client keeps the host's own positions and neither client is shown the serving engine's bookkeeping.
//  6. Assert the gateway bound both streams to their signed Finish by response_hash.
func TestE2E_LogprobsStayWithheldFromSilentClientsWithTheOptimizationOff(t *testing.T) {
	env, client := startProcessedStreamEnv(t, "false")

	silent := testutil.SendStreamingCompletion(t, client, env.clientURL, "logprobs not asked, optimization off")
	asking := testutil.SendStreamingCompletionWithLogprobs(t, client, env.clientURL, "logprobs asked, optimization off", 1)

	require.False(t, testutil.StreamCarries(silent, `"logprobs"`), "forwarding the stored bytes must not leak logprobs to a silent client: %v", silent.Events)
	require.True(t, testutil.StreamCarries(asking, `"bytes"`), "an asking client must get the host's own positions: %v", asking.Events)
	require.True(t, testutil.StreamCarries(asking, `"top_logprobs"`), "an asking client must get the alternatives: %v", asking.Events)
	testutil.RequireNoEngineBookkeeping(t, silent)
	testutil.RequireNoEngineBookkeeping(t, asking)
	requireEveryStreamBound(t, client, env.clientURL)
}
