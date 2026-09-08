package inference

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// The executor's two outputs diverge: the gateway is sent logprobs only when it asked; the store always keeps them.
func TestTheGatewayGetsStreamedLogprobsOnlyWhenItAsked(t *testing.T) {
	// An alternative's bytes must spell its token, or the executor rightly refuses to slim the chunk.
	spell := func(token string) string {
		spelled := make([]string, len(token))
		for index := range token {
			spelled[index] = strconv.Itoa(int(token[index]))
		}
		return "[" + strings.Join(spelled, ",") + "]"
	}
	chunk := func(content, token string) string {
		return `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","prompt_token_ids":[1,2],` +
			`"choices":[{"index":0,"delta":{"content":"` + content + `"},"token_ids":[3],` +
			`"logprobs":{"content":[{"token":"` + token + `","logprob":-0.5,"bytes":[72],` +
			`"top_logprobs":[{"token":"` + token + `","logprob":-0.5,"bytes":` + spell(token) + `},` +
			`{"token":"9","logprob":-9999,"bytes":` + spell("9") + `}]}]},` +
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

	for _, testCase := range []struct {
		name          string
		prompt        string
		wantForwarded bool
	}{
		{
			name:   "the gateway asked for none",
			prompt: `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name:          "the gateway asked for both",
			prompt:        `{"model":"m","stream":true,"logprobs":true,"top_logprobs":5,"messages":[{"role":"user","content":"hi"}]}`,
			wantForwarded: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := &recordingPayloadStore{}
			toGateway := httptest.NewRecorder()
			_, err := executeInference(context.Background(),
				devshardpkg.ExecuteRequest{
					InferenceID: 1, EscrowID: "60453", Model: "m",
					Prompt:         []byte(testCase.prompt),
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
			for _, dropped := range []string{"token_ids", "prompt_token_ids", "prompt_logprobs"} {
				if strings.Contains(forwarded, dropped) {
					t.Fatalf("%q reached the gateway: %s", dropped, forwarded)
				}
			}
			if carried := strings.Contains(forwarded, `"logprobs"`); carried != testCase.wantForwarded {
				t.Fatalf("logprobs reached the gateway = %t, want %t: %s", carried, testCase.wantForwarded, forwarded)
			}
			if testCase.wantForwarded && !strings.Contains(forwarded, `"bytes"`) {
				t.Fatalf("the gateway was handed the slimmed copy: %s", forwarded)
			}
			if !strings.Contains(forwarded, `"content":"Hi"`) {
				t.Fatalf("the answer did not reach the gateway: %s", forwarded)
			}

			// The keys live inside JSON strings in the envelope, where a search of the raw blob never
			// matches them, so each stored event is decoded and inspected as the object it is.
			for _, storedChunk := range storedChunks(t, store.responsePayload) {
				for _, dropped := range []string{"bytes", "token_ids", "prompt_token_ids", "prompt_logprobs"} {
					if bytes.Contains(mustMarshal(t, storedChunk), []byte(`"`+dropped+`"`)) {
						t.Fatalf("the stored payload kept %q: %v", dropped, storedChunk)
					}
				}
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
		})
	}
}

// storedChunks decodes the events of a stored streamed envelope so assertions land on the chunk
// objects rather than on the envelope's escaped bytes.
func storedChunks(t *testing.T, payload []byte) []map[string]any {
	t.Helper()
	var envelope struct {
		Events []string `json:"events"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("the stored payload is not a streamed envelope: %v", err)
	}
	var chunks []map[string]any
	for _, event := range envelope.Events {
		body := strings.TrimSpace(strings.TrimPrefix(event, "data: "))
		if body == "" || strings.HasPrefix(body, "[DONE]") {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(body), &chunk); err != nil {
			t.Fatalf("a stored event is not JSON: %v", err)
		}
		chunks = append(chunks, chunk)
	}
	if len(chunks) == 0 {
		t.Fatal("the stored envelope carries no data events")
	}
	return chunks
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return encoded
}

// A host may answer with plain JSON while the client is streaming, and that relay is its own branch:
// it must hand the gateway the forwarded copy, not the slimmed one the store keeps.
func TestAJSONHostRelayedToAStreamingClientCarriesLogprobsOnlyWhenAsked(t *testing.T) {
	body := `{"id":"x","object":"chat.completion","created":1786458557,"model":"m","prompt_token_ids":[1,2],` +
		`"choices":[{"index":0,"finish_reason":"length","token_ids":[3],"message":{"role":"assistant","content":"Hi"},` +
		`"logprobs":{"content":[{"token":"258","logprob":-0.5,"bytes":[32,97],` +
		`"top_logprobs":[{"token":"258","logprob":-0.5,"bytes":[50,53,56]},{"token":"9","logprob":-9999,"bytes":[57]}]}]}}],` +
		`"usage":{"prompt_tokens":7,"completion_tokens":1}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	for _, testCase := range []struct {
		name          string
		prompt        string
		wantForwarded bool
	}{
		{
			name:   "the gateway asked for none",
			prompt: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name:          "the gateway asked for both",
			prompt:        `{"model":"m","logprobs":true,"top_logprobs":5,"messages":[{"role":"user","content":"hi"}]}`,
			wantForwarded: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := &recordingPayloadStore{}
			toGateway := httptest.NewRecorder()
			_, err := executeInference(context.Background(),
				devshardpkg.ExecuteRequest{
					InferenceID: 1, EscrowID: "60453", Model: "m",
					Prompt:         []byte(testCase.prompt),
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

			relayed := toGateway.Body.String()
			if carried := strings.Contains(relayed, `"logprobs"`); carried != testCase.wantForwarded {
				t.Fatalf("logprobs reached the gateway = %t, want %t: %s", carried, testCase.wantForwarded, relayed)
			}
			if testCase.wantForwarded && !strings.Contains(relayed, `"bytes"`) {
				t.Fatalf("the gateway was handed the slimmed copy: %s", relayed)
			}
			for _, dropped := range []string{"token_ids", "prompt_token_ids"} {
				if strings.Contains(relayed, dropped) {
					t.Fatalf("%q reached the gateway: %s", dropped, relayed)
				}
			}
			if strings.Contains(string(store.responsePayload), `"bytes"`) {
				t.Fatalf("the stored payload kept what no validator reads: %s", store.responsePayload)
			}
			if !strings.Contains(string(store.responsePayload), "logprobs") {
				t.Fatalf("the stored payload lost what the validator replays against: %s", store.responsePayload)
			}
		})
	}
}
