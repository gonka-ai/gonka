package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
)

// Test flow:
//  1. Configure the harness with a 30-second snapshot max age.
//  2. Set the chain snapshot's last-healthy time to an hour ago.
//  3. Send a chat completion request.
//  4. Assert the response is 503 with a Retry-After header.
func TestChatIsRefusedWhenTheChainSnapshotIsTooOld(t *testing.T) {
	harness := newHarness(t, func(configuration *config.Config) {
		configuration.Chain.SnapshotMaxAgeSeconds = 30
	})
	harness.snapshots.snapshot = chain.PhaseSnapshot{
		LastHealthyAt: harnessClock.Add(-time.Hour),
	}

	recorder := harness.request(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"qwen","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"Authorization": "Bearer " + clientKey})

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", recorder.Code, recorder.Body)
	}
	if got := recorder.Header().Get("Retry-After"); got == "" {
		t.Errorf("Retry-After = %q, want the observer's poll interval", got)
	}
}

// Test flow:
//  1. Configure the harness with a 30-second snapshot max age.
//  2. Set the chain snapshot's last-healthy time to an hour ago.
//  3. Request the /v1/status endpoint.
//  4. Assert the response body reports requests_blocked and names the stale-snapshot block reason.
func TestStatusNamesAStaleSnapshotAsTheReasonItIsBlocked(t *testing.T) {
	harness := newHarness(t, func(configuration *config.Config) {
		configuration.Chain.SnapshotMaxAgeSeconds = 30
	})
	harness.snapshots.snapshot = chain.PhaseSnapshot{
		LastHealthyAt: harnessClock.Add(-time.Hour),
	}

	recorder := harness.request(t, http.MethodGet, "/v1/status", "",
		map[string]string{"Authorization": "Bearer " + clientKey})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	body := recorder.Body.String()
	for _, want := range []string{`"requests_blocked":true`, string(chain.BlockReasonSnapshotStale)} {
		if !strings.Contains(body, want) {
			t.Errorf("status body does not carry %s: %s", want, body)
		}
	}
}
