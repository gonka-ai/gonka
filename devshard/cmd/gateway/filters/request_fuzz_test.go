package filters

import (
	"encoding/json"
	"testing"
)

func FuzzNormalizeRequestHoldsItsContract(f *testing.F) {
	for _, seed := range []string{
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","messages":[],"stream":true,"max_tokens":10}`,
		`{"model":"m","messages":[],"logprobs":false,"top_logprobs":0}`,
		`{"model":"m","messages":[],"return_token_ids":false}`,
		`{"model":"m","messages":[],"structured_outputs":{"regex":"a+"}}`,
		`{"model":"m","messages":[],"n":3,"seed":9007199254740993}`,
		`{"model":"m","messages":[],"chat_template_kwargs":{"chat_template":"{{evil}}"}}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"x"}]}]}`,
		`{}`, `[]`, `"x"`, `{"model":1}`, `{"messages":"no"}`,
	} {
		f.Add(seed)
	}

	options := Options{DefaultMaxTokens: 100, MaxTokensCap: 1000}

	f.Fuzz(func(t *testing.T, body string) {
		result, err := NormalizeRequest([]byte(body), options)
		if err != nil {
			return
		}
		if !json.Valid(result.Body) {
			t.Fatalf("accepted %q and produced invalid JSON: %s", body, result.Body)
		}
		var decoded map[string]any
		if err := json.Unmarshal(result.Body, &decoded); err != nil {
			t.Fatalf("accepted body does not decode as an object: %s: %v", result.Body, err)
		}
		if got, held := decoded["logprobs"]; held && got != true {
			t.Fatalf("client kept control of logprobs: got %v, want the forced true, from %q", got, body)
		}
		if got, held := decoded["return_token_ids"]; held {
			t.Fatalf("return_token_ids %v reached the host, which sets it itself, from %q", got, body)
		}
	})
}
