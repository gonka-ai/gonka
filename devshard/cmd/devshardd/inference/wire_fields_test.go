package inference

import (
	"encoding/json"
	"strings"
	"testing"
)

// The gateway drops all four of these on arrival, so carrying them across the network buys nothing.
func TestWireFieldsAreDroppedFromWhatTheGatewaySees(t *testing.T) {
	t.Parallel()
	chunk := `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","prompt_token_ids":[1,2],"prompt_logprobs":[{"a":1}],"choices":[{"index":0,"delta":{"content":"Hi"},"token_ids":[3],"logprobs":{"content":[{"token":"258","logprob":-0.5,"top_logprobs":[{"token":"258","logprob":-0.5}]}]},"finish_reason":null}],"usage":{"prompt_tokens":7}}`

	stripped := stripWireOnlyFields(chunk)
	for _, dropped := range []string{"prompt_token_ids", "prompt_logprobs", "token_ids", "logprobs"} {
		if strings.Contains(stripped, dropped) {
			t.Fatalf("%q survived the strip: %s", dropped, stripped)
		}
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(stripped, "data: ")), &decoded); err != nil {
		t.Fatalf("the stripped chunk no longer parses: %v", err)
	}
	choice := decoded["choices"].([]any)[0].(map[string]any)
	if delta := choice["delta"].(map[string]any); delta["content"] != "Hi" {
		t.Fatalf("the answer itself changed: %+v", delta)
	}
	if _, present := choice["finish_reason"]; !present {
		t.Fatal("finish_reason went with the strip")
	}
	if usage := decoded["usage"].(map[string]any); usage["prompt_tokens"] == nil {
		t.Fatal("usage went with the strip")
	}
	if decoded["id"] != "x" || decoded["model"] != "m" {
		t.Fatalf("chunk housekeeping changed: %+v", decoded)
	}
}

// Lines the stream carries that are not JSON must pass through untouched.
func TestWireStripLeavesNonPayloadLinesAlone(t *testing.T) {
	t.Parallel()
	for _, line := range []string{"", "data: [DONE]", ": keep-alive", "event: ping"} {
		if got := stripWireOnlyFields(line); got != line {
			t.Fatalf("stripWireOnlyFields(%q) = %q, want it unchanged", line, got)
		}
	}
}

// A chunk that does not parse is forwarded as it came: the gateway decides what to do with it, and a
// strip that swallowed it would turn a host's malformed answer into a silent empty one.
func TestWireStripForwardsWhatItCannotParse(t *testing.T) {
	t.Parallel()
	broken := `data: {"choices":[{"delta":`
	if got := stripWireOnlyFields(broken); got != broken {
		t.Fatalf("stripWireOnlyFields(broken) = %q, want it unchanged", got)
	}
}
