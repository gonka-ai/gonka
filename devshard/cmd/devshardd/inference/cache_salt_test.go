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

func saltTestBody() []byte {
	return []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
}

func TestWithCacheSaltStampsWithoutDisturbingTheRequest(t *testing.T) {
	var fields map[string]any
	if err := json.Unmarshal(withCacheSalt(saltTestBody(), "escrow-1", "sess-A"), &fields); err != nil {
		t.Fatalf("output not valid json: %v", err)
	}

	if salt, ok := fields["cache_salt"].(string); !ok || salt == "" {
		t.Fatalf("cache_salt not set, got %v", fields["cache_salt"])
	}
	if fields["model"] != "m" {
		t.Fatalf("original fields must be preserved; model=%v", fields["model"])
	}
}

func TestWithCacheSaltIsDeterministicPerSession(t *testing.T) {
	body := saltTestBody()

	first := withCacheSalt(body, "escrow-1", "sess-A")
	second := withCacheSalt(body, "escrow-1", "sess-A")

	if string(first) != string(second) {
		t.Fatal("a session's own follow-ups must reuse its cache, so the salt must not vary")
	}
}

func TestWithCacheSaltSeparatesSessionsSharingOneEscrow(t *testing.T) {
	body := saltTestBody()

	if saltOf(t, body, "escrow-1", "sess-B") == saltOf(t, body, "escrow-1", "sess-A") {
		t.Fatal("two sessions must not share KV blocks")
	}
}

func TestWithCacheSaltSeparatesEscrowsSharingOneSessionID(t *testing.T) {
	body := saltTestBody()

	if saltOf(t, body, "escrow-1", "sess-A") == saltOf(t, body, "escrow-2", "sess-A") {
		t.Fatal("one session id under two escrows must not share a prefix-cache namespace")
	}
}

func TestWithCacheSaltPassesAnUnparseableBodyThrough(t *testing.T) {
	unparseable := []byte(`{not json`)

	if string(withCacheSalt(unparseable, "escrow-1", "s")) != string(unparseable) {
		t.Fatal("salting is best-effort and must never break a request")
	}
}
