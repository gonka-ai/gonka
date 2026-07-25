package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"devshard/e2e/testutil"
)

// Test flow:
//  1. Start the default three-host environment.
//  2. Send one streaming OpenAI-style chat completion through devshardctl.
//  3. Assert the response is an SSE stream with content-bearing data chunks.
//  4. Assert the stream ends with [DONE].
//  5. Assert devshard protocol metadata does not leak into the client stream.
//  6. Finalize the session and verify the stable settlement contract.
func TestE2E_StreamingChatCompletion(t *testing.T) {
	requireE2EEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	images := requiredImages(t)
	env := startHappyPathEnv(ctx, t, images)

	client := &http.Client{Timeout: testutil.DefaultRequestTimeout}
	stream := testutil.SendStreamingCompletion(t, client, env.clientURL, "stream smoke")
	testutil.RequireOpenAIStream(t, stream)

	settlement := testutil.FinalizeSession(t, client, env.clientURL)
	testutil.RequireSettlementContract(t, settlement)
}

// Test flow:
//  1. Start the default three-host environment.
//  2. Send a streaming completion through devshardctl.
//  3. Send the same streaming completion again.
//  4. Assert both responses keep valid OpenAI-compatible SSE shape.
//  5. Assert the possible cached response still ends with [DONE] and is not
//     replayed as raw non-streaming JSON.
func TestE2E_StreamingCacheHitKeepsSSEShape(t *testing.T) {
	requireE2EEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	images := requiredImages(t)
	env := startHappyPathEnv(ctx, t, images)

	client := &http.Client{Timeout: testutil.DefaultRequestTimeout}
	content := "stream cache shape"
	first := testutil.SendStreamingCompletion(t, client, env.clientURL, content)
	testutil.RequireOpenAIStream(t, first)

	second := testutil.SendStreamingCompletion(t, client, env.clientURL, content)
	testutil.RequireOpenAIStream(t, second)
}

// Test flow:
//  1. Start the default three-host environment.
//  2. Send a streaming completion through devshardctl.
//  3. Send the same prompt as a non-streaming completion.
//  4. Assert streaming stays SSE and non-streaming stays JSON.
//  5. Assert streaming and non-streaming cache entries do not contaminate each
//     other's response shape.
func TestE2E_StreamingThenNonStreamingCacheIsolation(t *testing.T) {
	requireE2EEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	images := requiredImages(t)
	env := startHappyPathEnv(ctx, t, images)

	client := &http.Client{Timeout: testutil.DefaultRequestTimeout}
	content := "stream then json cache isolation"
	stream := testutil.SendStreamingCompletion(t, client, env.clientURL, content)
	testutil.RequireOpenAIStream(t, stream)

	resp := testutil.SendCompletion(t, client, env.clientURL, content)
	testutil.RequireOpenAICompletion(t, resp)
}

// Test flow:
//  1. Start the default three-host environment.
//  2. Send a non-streaming completion through devshardctl.
//  3. Send the same prompt as a streaming completion.
//  4. Assert non-streaming stays JSON and streaming stays SSE.
//  5. Assert cached JSON is not replayed as a raw non-streaming object in the
//     SSE stream.
func TestE2E_NonStreamingThenStreamingCacheIsolation(t *testing.T) {
	requireE2EEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	images := requiredImages(t)
	env := startHappyPathEnv(ctx, t, images)

	client := &http.Client{Timeout: testutil.DefaultRequestTimeout}
	content := "json then stream cache isolation"
	resp := testutil.SendCompletion(t, client, env.clientURL, content)
	testutil.RequireOpenAICompletion(t, resp)

	stream := testutil.SendStreamingCompletion(t, client, env.clientURL, content)
	testutil.RequireOpenAIStream(t, stream)
}
