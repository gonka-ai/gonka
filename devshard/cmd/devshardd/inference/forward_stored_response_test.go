package inference

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"common/completionapi"
	devshardpkg "devshard"
)

var answeredChunks = []string{
	`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m",` +
		`"choices":[{"index":0,"delta":{"content":"Hi"},"token_ids":[258],"finish_reason":null,` +
		`"logprobs":{"content":[{"token":"Hi","logprob":-0.5,"bytes":[72,105],"top_logprobs":[` +
		`{"token":"Hi","logprob":-0.5,"bytes":[72,105]}]}]}}]}`,
	`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m",` +
		`"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":1}}`,
	"data: [DONE]",
}

func runStubbedInference(t *testing.T, request devshardpkg.ExecuteRequest, logprobsOptimizationEnabled bool, chunks ...string) (*devshardpkg.ExecuteResult, string) {
	t.Helper()
	inference := runServedRequest(t, request, "text/event-stream", logprobsOptimizationEnabled, chunks...)
	return inference.result, inference.forwarded
}

func dataLines(stream string) []string {
	var lines []string
	for _, line := range strings.Split(stream, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, completionapi.DataPrefix) {
			continue
		}
		if strings.Contains(line, `"devshard_receipt"`) || strings.Contains(line, `"devshard_meta"`) {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func TestTheGatewaysChoiceOverridesTheExecutorDefault(t *testing.T) {
	optimize, forwardStored := true, false
	for _, testCase := range []struct {
		name            string
		override        *bool
		executorDefault bool
		wantSameHash    bool
	}{
		{name: "silent gateway, executor optimizes", executorDefault: true},
		{name: "silent gateway, executor forwards stored", executorDefault: false, wantSameHash: true},
		{name: "gateway asks to optimize against the executor default", override: &optimize, executorDefault: false},
		{name: "gateway asks for the stored bytes against the executor default", override: &forwardStored, executorDefault: true, wantSameHash: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := devshardpkg.ExecuteRequest{
				InferenceID: 1, EscrowID: "60453", Model: "m",
				Prompt:                       []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
				LogprobsOptimizationOverride: testCase.override,
			}
			result, forwarded := runStubbedInference(t, request, testCase.executorDefault, answeredChunks...)

			rebuilt, err := json.Marshal(completionapi.SerializedStreamedResponse{Events: dataLines(forwarded)})
			if err != nil {
				t.Fatalf("rebuild the forwarded stream: %v", err)
			}
			rebuiltHash := sha256.Sum256(rebuilt)
			if sameHash := string(rebuiltHash[:]) == string(result.ResponseHash); sameHash != testCase.wantSameHash {
				t.Fatalf("rehashed to the committed hash: %v, want %v\nrebuilt: %s", sameHash, testCase.wantSameHash, rebuilt)
			}
		})
	}
}

func TestAJSONErrorRelayedToAStreamingClientCannotBeRehashed(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		body        string
		wantFinish  bool
		wantErrorIs string
	}{
		{
			name:        "no usage, the inference never finishes",
			body:        `{"error":{"code":400,"message":"context length exceeded","type":"BadRequestError"}}`,
			wantErrorIs: "no usage found",
		},
		{
			name:       "usage present, a finish is committed",
			body:       `{"error":{"code":400,"message":"context length exceeded","type":"BadRequestError"},"usage":{"prompt_tokens":10,"completion_tokens":0}}`,
			wantFinish: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()

			toGateway := httptest.NewRecorder()
			result, err := executeInference(context.Background(),
				devshardpkg.ExecuteRequest{
					InferenceID: 1, EscrowID: "60453", Model: "m",
					Prompt:         []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
					ResponseWriter: toGateway,
				},
				&recordingPayloadStore{}, 1,
				func(ctx context.Context, _ string, requestBody []byte) (*http.Response, error) {
					call, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(string(requestBody)))
					return http.DefaultClient.Do(call)
				},
				fixedChainParams{}, false)

			if !testCase.wantFinish {
				if err == nil || !strings.Contains(err.Error(), testCase.wantErrorIs) {
					t.Fatalf("executeInference: %v, want an error containing %q", err, testCase.wantErrorIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("executeInference: %v", err)
			}

			rebuilt, marshalErr := json.Marshal(completionapi.SerializedStreamedResponse{Events: dataLines(toGateway.Body.String())})
			if marshalErr != nil {
				t.Fatalf("rebuild the forwarded stream: %v", marshalErr)
			}
			rebuiltHash := sha256.Sum256(rebuilt)
			if string(rebuiltHash[:]) == string(result.ResponseHash) {
				t.Fatal("the envelope now rehashes to the committed hash: the relay stores what it emits, so this limitation is gone")
			}
		})
	}
}
