package inference

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"common/completionapi"
	devshardpkg "devshard"

	"github.com/stretchr/testify/require"
)

const (
	streamingPrompt               = `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	streamingPromptAskingLogprobs = `{"model":"m","stream":true,"logprobs":true,"top_logprobs":1,"messages":[{"role":"user","content":"hi"}]}`
)

type servedInference struct {
	result    *devshardpkg.ExecuteResult
	stored    []byte
	forwarded string
}

func runServedInference(t *testing.T, prompt, contentType string, optimizationEnabled bool, chunks ...string) servedInference {
	t.Helper()
	request := devshardpkg.ExecuteRequest{InferenceID: 1, EscrowID: "60453", Model: "m", Prompt: []byte(prompt)}
	return runServedRequest(t, request, contentType, optimizationEnabled, chunks...)
}

func runServedRequest(t *testing.T, request devshardpkg.ExecuteRequest, contentType string, optimizationEnabled bool, chunks ...string) servedInference {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		for _, chunk := range chunks {
			_, _ = w.Write([]byte(chunk + "\n\n"))
		}
	}))
	defer server.Close()

	store := &recordingPayloadStore{}
	toGateway := httptest.NewRecorder()
	request.ResponseWriter = toGateway
	result, err := executeInference(context.Background(), request, store, 1,
		func(ctx context.Context, _ string, requestBody []byte) (*http.Response, error) {
			call, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(string(requestBody)))
			if err != nil {
				return nil, err
			}
			return http.DefaultClient.Do(call)
		},
		fixedChainParams{}, optimizationEnabled)
	require.NoError(t, err)
	return servedInference{result: result, stored: store.responsePayload, forwarded: toGateway.Body.String()}
}

func receivedSums(forwarded string) [][32]byte {
	hasher := completionapi.NewReceivedResponseHasher()
	for _, line := range dataLines(forwarded) {
		hasher.Add(line)
	}
	return hasher.Sums()
}

// Test flow:
//  1. Execute a streamed inference against a stub ML node with the case's prompt and optimization setting.
//  2. Assert response_hash covers the stored payload and served_hash covers StripForGateway of it.
//  3. Hash the lines written to the gateway and assert they match response_hash when the gateway asked or the optimization is off, served_hash otherwise.
func TestTheFinishHashesBindWhatTheGatewayReceived(t *testing.T) {
	for _, testCase := range []struct {
		name                string
		prompt              string
		optimizationEnabled bool
		wantReceivedStored  bool
	}{
		{name: "optimized, gateway did not ask for logprobs", prompt: streamingPrompt, optimizationEnabled: true},
		{name: "optimized, gateway asked for logprobs", prompt: streamingPromptAskingLogprobs, optimizationEnabled: true, wantReceivedStored: true},
		{name: "not optimized, gateway did not ask for logprobs", prompt: streamingPrompt, wantReceivedStored: true},
		{name: "not optimized, gateway asked for logprobs", prompt: streamingPromptAskingLogprobs, wantReceivedStored: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			inference := runServedInference(t, testCase.prompt, "text/event-stream", testCase.optimizationEnabled, answeredChunks...)

			require.Equal(t, sha256.Sum256(inference.stored), [32]byte(inference.result.ResponseHash))
			stripped, err := completionapi.StripForGateway(inference.stored)
			require.NoError(t, err)
			require.Equal(t, sha256.Sum256(stripped), [32]byte(inference.result.ServedHash),
				"a validator re-derives served_hash from the stored payload")

			wantHash := inference.result.ServedHash
			if testCase.wantReceivedStored {
				wantHash = inference.result.ResponseHash
			}
			require.Contains(t, receivedSums(inference.forwarded), [32]byte(wantHash),
				"the gateway rehashes what it received to a signed hash:\n%s", inference.forwarded)
		})
	}
}

// Test flow:
//  1. Execute a streaming request against an ML node that answers with a plain JSON error body carrying usage.
//  2. Hash what the executor relayed to the gateway.
//  3. Assert it matches one of the two signed hashes.
func TestAJSONBodyRelayedToAStreamingGatewayIsBound(t *testing.T) {
	body := `{"error":{"code":400,"message":"context length exceeded","type":"BadRequestError"},"usage":{"prompt_tokens":10,"completion_tokens":0}}`
	inference := runServedInference(t, streamingPrompt, "application/json", true, body)

	require.Contains(t, receivedSums(inference.forwarded), [32]byte(inference.result.ServedHash),
		"a gateway that did not ask gets the served view, relayed bare:\n%s", inference.forwarded)
}
