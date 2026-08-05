package inference

import (
	"encoding/json"
	"testing"
)

func saltOf(t *testing.T, body []byte, escrowID, sessionID string) string {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(withCacheSalt(body, escrowID, sessionID), &fields); err != nil {
		t.Fatalf("output not valid json: %v", err)
	}
	salt, ok := fields["cache_salt"].(string)
	if !ok || salt == "" {
		t.Fatalf("cache_salt not set, got %v", fields["cache_salt"])
	}
	return salt
}

func TestWithCacheSalt(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	out := withCacheSalt(body, "escrow-1", "sess-A")
	var fields map[string]any
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatalf("output not valid json: %v", err)
	}
	if salt, ok := fields["cache_salt"].(string); !ok || salt == "" {
		t.Fatalf("cache_salt not set, got %v", fields["cache_salt"])
	}
	if fields["model"] != "m" {
		t.Fatalf("original fields must be preserved; model=%v", fields["model"])
	}

	// Deterministic per session (so a client's own follow-ups reuse its cache).
	if string(withCacheSalt(body, "escrow-1", "sess-A")) != string(out) {
		t.Fatal("same escrow and session must yield the same salt")
	}

	// Different sessions get different salts (isolation — no shared KV blocks).
	if saltOf(t, body, "escrow-1", "sess-B") == saltOf(t, body, "escrow-1", "sess-A") {
		t.Fatal("different sessions must get different cache_salt")
	}

	// Unparseable body passes through unchanged (best-effort, never breaks a request).
	bad := []byte(`{not json`)
	if string(withCacheSalt(bad, "escrow-1", "s")) != string(bad) {
		t.Fatal("unparseable body must pass through unchanged")
	}
}

func TestWithCacheSaltSeparatesEscrowsSharingOneSessionID(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	first := saltOf(t, body, "escrow-1", "sess-A")
	second := saltOf(t, body, "escrow-2", "sess-A")

	if first == second {
		t.Fatal("one session id under two escrows must not share a prefix-cache namespace")
	}
}
