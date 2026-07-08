package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSyncWhitelistPreservesStateWhenNoIPsResolve proves that a transient
// resolution outage (participants exist but none resolve to a public IP) makes
// syncWhitelist return an error instead of overwriting the nginx whitelist with
// an empty set. The error keeps the caller from advancing BlockHeightSynced, so
// the sync retries within the same epoch rather than de-whitelisting every
// validator until the epoch flips.
func TestSyncWhitelistPreservesStateWhenNoIPsResolve(t *testing.T) {
	// Both hosts use the reserved .invalid TLD (RFC 6761), which never resolves.
	const body = `{"active_participants":{"participants":[{"inference_url":"http://sync-fail-1.invalid"},{"inference_url":"http://sync-fail-2.invalid"}]}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/epochs/current/participants" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	oldURL := ApiUrl
	ApiUrl = srv.URL
	defer func() { ApiUrl = oldURL }()

	err := syncWhitelist()
	if err == nil {
		t.Fatal("expected syncWhitelist to fail when no participant IPs resolve; a nil error would wipe the whitelist")
	}
	if !strings.Contains(err.Error(), "resolved 0 of 2") {
		t.Fatalf("expected a 'resolved 0 of 2' preservation error, got: %v", err)
	}
}
