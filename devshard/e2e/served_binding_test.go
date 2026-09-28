package e2e

import (
	"testing"

	"devshard/e2e/testutil"
	"devshard/internal/e2econfig"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Start the three-host environment with every executor storing and signing its real answer but streaming a changed one.
//  2. Send one streaming completion.
//  3. Assert the client received the changed answer, so the swap reached the wire.
//  4. Assert the gateway judged the stream a mismatch against the signed Finish and bound none.
func TestE2E_ServedBindingCatchesAStreamTheExecutorDidNotSign(t *testing.T) {
	env, client := startNonStreamingEnvWithOptions(t, e2eEnvOptions{hostEnv: map[string]string{
		e2econfig.StubInferenceTamperedStreamEnv: "true",
		"DEVSHARD_LOGPROBS_OPTIMIZATION_ENABLED": "true",
	}})

	stream := testutil.SendStreamingCompletion(t, client, env.clientURL, "served binding against a swapped stream")
	testutil.DebugLogf(t, "client stream: %v", stream.Events)
	require.True(t, testutil.StreamCarries(stream, "tampered"), "the swapped answer never reached the client: %v", stream.Events)

	bindings := testutil.WaitServedBindings(t, client, env.clientURL, servedBindingWindow, func(bindings testutil.ServedBindings) bool {
		return bindings["mismatch"] >= 1
	})
	require.GreaterOrEqual(t, bindings["mismatch"], float64(1), "a stream the executor did not sign must be a mismatch: %v", bindings)
	require.Zero(t, bindings["bound"], "no swapped stream may bind: %v", bindings)
}
