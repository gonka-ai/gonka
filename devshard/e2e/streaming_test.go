package e2e

import (
	"testing"

	"devshard/e2e/testutil"
)

// Test flow:
//  1. Start the default three-host environment.
//  2. Send one streaming OpenAI-style chat completion through the gateway.
//  3. Assert the response is an SSE stream with content-bearing data chunks.
//  4. Assert the stream ends with [DONE].
//  5. Assert devshard protocol metadata does not leak into the client stream.
//  6. Finalize the escrow and assert it left the active phase at a finalize nonce with signature quorum.
func TestE2E_StreamingChatCompletion(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	stream := testutil.SendStreamingCompletion(t, client, env.clientURL, "stream smoke")
	testutil.RequireOpenAIStream(t, stream)

	testutil.FinalizeSession(t, client, env.escrowURL())
	testutil.RequireFinalized(t, client, env.escrowURL())
}

// Test flow:
//  1. Start the default three-host environment.
//  2. Send a streaming completion through the gateway.
//  3. Send the same streaming completion again.
//  4. Assert both responses keep valid OpenAI-compatible SSE shape.
//  5. Assert the possible cached response still ends with [DONE] and is not
//     replayed as raw non-streaming JSON.
func TestE2E_StreamingCacheHitKeepsSSEShape(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	content := "stream cache shape"
	first := testutil.SendStreamingCompletion(t, client, env.clientURL, content)
	testutil.RequireOpenAIStream(t, first)

	second := testutil.SendStreamingCompletion(t, client, env.clientURL, content)
	testutil.RequireOpenAIStream(t, second)
}

// Test flow:
//  1. Start the default three-host environment.
//  2. Send a streaming completion through the gateway.
//  3. Send the same prompt as a non-streaming completion.
//  4. Assert streaming stays SSE and non-streaming stays JSON.
//  5. Assert streaming and non-streaming cache entries do not contaminate each
//     other's response shape.
func TestE2E_StreamingThenNonStreamingCacheIsolation(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	content := "stream then json cache isolation"
	stream := testutil.SendStreamingCompletion(t, client, env.clientURL, content)
	testutil.RequireOpenAIStream(t, stream)

	resp := testutil.SendCompletion(t, client, env.clientURL, content)
	testutil.RequireOpenAICompletion(t, resp)
}

// Test flow:
//  1. Start the default three-host environment.
//  2. Send a non-streaming completion through the gateway.
//  3. Send the same prompt as a streaming completion.
//  4. Assert non-streaming stays JSON and streaming stays SSE.
//  5. Assert cached JSON is not replayed as a raw non-streaming object in the
//     SSE stream.
func TestE2E_NonStreamingThenStreamingCacheIsolation(t *testing.T) {
	env, client := startGatewayEnv(t, e2eEnvOptions{})

	content := "json then stream cache isolation"
	resp := testutil.SendCompletion(t, client, env.clientURL, content)
	testutil.RequireOpenAICompletion(t, resp)

	stream := testutil.SendStreamingCompletion(t, client, env.clientURL, content)
	testutil.RequireOpenAIStream(t, stream)
}
