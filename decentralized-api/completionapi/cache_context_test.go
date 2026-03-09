package completionapi

import (
	"encoding/json"
	"strings"
	"testing"
)

// ── ExtractCachedContent ──────────────────────────────────────────────────────

// TestExtractCachedContent_Valid verifies that a well-formed completion response
// returns the assistant message content.
func TestExtractCachedContent_Valid(t *testing.T) {
	resp := Response{
		Choices: []Choice{
			{Message: &Message{Role: "assistant", Content: "Here is the fix:\n```go\nsync.Mutex\n```"}},
		},
	}
	b, _ := json.Marshal(resp)

	content, ok := ExtractCachedContent(b)
	if !ok {
		t.Fatal("expected ok=true for valid response")
	}
	if !strings.Contains(content, "sync.Mutex") {
		t.Errorf("expected content to contain 'sync.Mutex', got: %q", content)
	}
}

// TestExtractCachedContent_EmptyContent verifies that an empty assistant message
// returns ok=false — we should not inject an empty context string.
func TestExtractCachedContent_EmptyContent(t *testing.T) {
	resp := Response{
		Choices: []Choice{
			{Message: &Message{Role: "assistant", Content: ""}},
		},
	}
	b, _ := json.Marshal(resp)

	_, ok := ExtractCachedContent(b)
	if ok {
		t.Fatal("expected ok=false for empty content")
	}
}

// TestExtractCachedContent_NoChoices returns ok=false when choices list is empty.
func TestExtractCachedContent_NoChoices(t *testing.T) {
	resp := Response{Choices: []Choice{}}
	b, _ := json.Marshal(resp)

	_, ok := ExtractCachedContent(b)
	if ok {
		t.Fatal("expected ok=false for empty choices")
	}
}

// TestExtractCachedContent_InvalidJSON returns ok=false on malformed bytes.
func TestExtractCachedContent_InvalidJSON(t *testing.T) {
	_, ok := ExtractCachedContent([]byte("not json"))
	if ok {
		t.Fatal("expected ok=false for invalid JSON")
	}
}

// ── InjectCachedContext ───────────────────────────────────────────────────────

// TestInjectCachedContext_PrependsSysMsg verifies that a system message is
// prepended to the messages array and the original messages are preserved.
func TestInjectCachedContext_PrependsSysMsg(t *testing.T) {
	body := []byte(`{"model":"gpt","messages":[{"role":"user","content":"Fix race in RateLimiter"}]}`)

	result, err := InjectCachedContext(body, "Counter fix: add sync.Mutex")
	if err != nil {
		t.Fatalf("InjectCachedContext failed: %v", err)
	}

	var req map[string]interface{}
	if err := json.Unmarshal(result, &req); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	msgs, _ := req["messages"].([]interface{})
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(msgs))
	}

	sysMsg, _ := msgs[0].(map[string]interface{})
	if sysMsg["role"] != "system" {
		t.Errorf("expected first message role='system', got %v", sysMsg["role"])
	}
	content, _ := sysMsg["content"].(string)
	if !strings.Contains(content, "Counter fix: add sync.Mutex") {
		t.Errorf("system message should contain the cached content, got: %q", content)
	}

	userMsg, _ := msgs[1].(map[string]interface{})
	if userMsg["content"] != "Fix race in RateLimiter" {
		t.Errorf("original user message should be preserved, got: %v", userMsg["content"])
	}
}

// TestInjectCachedContext_EmptyMessages verifies that injection into an empty
// messages array produces a single system message (no panic).
func TestInjectCachedContext_EmptyMessages(t *testing.T) {
	body := []byte(`{"model":"gpt","messages":[]}`)
	result, err := InjectCachedContext(body, "context")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var req map[string]interface{}
	_ = json.Unmarshal(result, &req)
	msgs, _ := req["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (injected system), got %d", len(msgs))
	}
}

// TestInjectCachedContext_InvalidJSON verifies that invalid input returns an error.
func TestInjectCachedContext_InvalidJSON(t *testing.T) {
	_, err := InjectCachedContext([]byte("not json"), "ctx")
	if err == nil {
		t.Fatal("expected error for invalid JSON input")
	}
}

// TestInjectCachedContext_PreservesModelField ensures other request fields
// (model, max_tokens, seed) are not dropped by the injection.
func TestInjectCachedContext_PreservesModelField(t *testing.T) {
	body := []byte(`{"model":"Qwen2.5-7B","max_tokens":512,"seed":42,"messages":[{"role":"user","content":"Hi"}]}`)
	result, err := InjectCachedContext(body, "ctx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var req map[string]interface{}
	_ = json.Unmarshal(result, &req)

	if req["model"] != "Qwen2.5-7B" {
		t.Errorf("model field lost, got: %v", req["model"])
	}
	if req["max_tokens"].(float64) != 512 {
		t.Errorf("max_tokens field lost, got: %v", req["max_tokens"])
	}
}
