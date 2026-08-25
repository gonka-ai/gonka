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

type recordingPayloadStore struct {
	responsePayload []byte
}

func (s *recordingPayloadStore) Store(_ context.Context, _ string, _, _ uint64, _, responsePayload []byte) error {
	s.responsePayload = responsePayload
	return nil
}

type fixedChainParams struct{}

func (fixedChainParams) LogprobsMode() string { return "" }

// Slimming must precede the hash: a compress payload under a whole-body hash can never be verified.
func TestTheStoredPayloadIsSlimAndItsHashCoversTheSlimBytes(t *testing.T) {
	body := `{"id":"x","object":"chat.completion","created":1786458557,"model":"m","choices":[{"index":0,"finish_reason":"length","message":{"role":"assistant","content":"Hi"},"logprobs":{"content":[{"token":"258","logprob":-0.5,"bytes":[32,97],"top_logprobs":[{"token":"258","logprob":-0.5,"bytes":[50,53,56]},{"token":"0","logprob":-9999,"bytes":[48]}]}]}}],"usage":{"prompt_tokens":7,"completion_tokens":1}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	store := &recordingPayloadStore{}
	result, err := executeInference(context.Background(),
		devshardpkg.ExecuteRequest{
			InferenceID: 1,
			EscrowID:    "60453",
			Model:       "m",
			Prompt:      []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		},
		store, 1,
		func(ctx context.Context, _ string, requestBody []byte) (*http.Response, error) {
			request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(string(requestBody)))
			return http.DefaultClient.Do(request)
		},
		fixedChainParams{})
	if err != nil {
		t.Fatalf("executeInference: %v", err)
	}

	if store.responsePayload == nil {
		t.Fatal("nothing was stored")
	}
	stored := string(store.responsePayload)
	if strings.Contains(stored, `"bytes"`) {
		t.Fatalf("the stored payload still carries bytes: %s", stored)
	}
	if !strings.Contains(stored, `"content":"Hi"`) {
		t.Fatalf("the stored payload lost the answer: %s", stored)
	}

	hash := sha256.Sum256(store.responsePayload)
	if string(result.ResponseHash) != string(hash[:]) {
		t.Fatal("the committed hash does not cover the stored bytes")
	}

	response, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(store.responsePayload)
	if err != nil {
		t.Fatalf("a validator cannot parse the stored payload: %v", err)
	}
	enforced, err := response.GetEnforcedTokens()
	if err != nil {
		t.Fatalf("GetEnforcedTokens: %v", err)
	}
	if len(enforced.Tokens) != 1 || enforced.Tokens[0].Token != "258" {
		encoded, _ := json.Marshal(enforced)
		t.Fatalf("enforced tokens came back as %s", encoded)
	}
}

// The executor keeps every chunk whole for validation and forwards a stripped one, so the two outputs
// must diverge: no logprobs reach the gateway, and the stored payload still replays.
func TestTheGatewayGetsNoLogprobsWhileTheStoredPayloadKeepsThem(t *testing.T) {
	chunk := func(content, token string) string {
		return `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","prompt_token_ids":[1,2],` +
			`"choices":[{"index":0,"delta":{"content":"` + content + `"},"token_ids":[3],` +
			`"logprobs":{"content":[{"token":"` + token + `","logprob":-0.5,"bytes":[72],` +
			`"top_logprobs":[{"token":"` + token + `","logprob":-0.5,"bytes":[55]},{"token":"9","logprob":-9999,"bytes":[57]}]}]},` +
			`"finish_reason":null}]}`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range []string{
			chunk("Hi", "258"),
			chunk("!", "494"),
			`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`,
			"data: [DONE]",
		} {
			_, _ = w.Write([]byte(line + "\n\n"))
		}
	}))
	defer server.Close()

	store := &recordingPayloadStore{}
	toGateway := httptest.NewRecorder()
	_, err := executeInference(context.Background(),
		devshardpkg.ExecuteRequest{
			InferenceID: 1, EscrowID: "60453", Model: "m",
			Prompt:         []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
			ResponseWriter: toGateway,
		},
		store, 1,
		func(ctx context.Context, _ string, requestBody []byte) (*http.Response, error) {
			request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(string(requestBody)))
			return http.DefaultClient.Do(request)
		},
		fixedChainParams{})
	if err != nil {
		t.Fatalf("executeInference: %v", err)
	}

	forwarded := toGateway.Body.String()
	for _, dropped := range []string{"logprobs", "token_ids", "prompt_token_ids"} {
		if strings.Contains(forwarded, dropped) {
			t.Fatalf("%q reached the gateway: %s", dropped, forwarded)
		}
	}
	if !strings.Contains(forwarded, `"content":"Hi"`) {
		t.Fatalf("the answer did not reach the gateway: %s", forwarded)
	}

	stored := string(store.responsePayload)
	if strings.Contains(stored, `"bytes"`) {
		t.Fatalf("the stored payload kept the fields validation never reads: %s", stored)
	}
	response, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(store.responsePayload)
	if err != nil {
		t.Fatalf("a validator cannot parse the stored payload: %v", err)
	}
	enforced, err := response.GetEnforcedTokens()
	if err != nil {
		t.Fatalf("GetEnforcedTokens: %v", err)
	}
	if len(enforced.Tokens) != 2 || enforced.Tokens[0].Token != "258" || enforced.Tokens[1].Token != "494" {
		encoded, _ := json.Marshal(enforced)
		t.Fatalf("the stored payload no longer replays the executor's token path: %s", encoded)
	}
	if len(enforced.Tokens[0].TopTokens) != 2 {
		t.Fatalf("the alternatives a validator pins its replay to went missing: %+v", enforced.Tokens[0])
	}
}
