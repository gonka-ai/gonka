package container

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// composeHasHeightSyncEmit scans recent devshardctl compose logs for heightsync: emit.
func composeHasHeightSyncEmit(t *testing.T, ws, project string, since time.Time, wantNonce int, direction, mode string) bool {
	t.Helper()
	for _, ln := range composeLogsSince(t, ws, project, "devshardctl", since, 600) {
		kv := parseLogPayloadFromLine(ln)
		if kv["msg"] != "heightsync: emit" || kv["direction"] != direction {
			continue
		}
		if !strings.EqualFold(kv["mode"], mode) {
			continue
		}
		if parseNonce(kv["nonce"]) == wantNonce {
			return true
		}
	}
	return false
}

// assertHeightSyncRequestEmit checks compose (fast) then Loki for devshardctl request emit.
func assertHeightSyncRequestEmit(
	t *testing.T, ws, project string, lokiClient *http.Client,
	wantNonce int, mode string, since time.Time,
) {
	t.Helper()
	if composeHasHeightSyncEmit(t, ws, project, since, wantNonce, "request", mode) {
		t.Logf("compose devshardctl: saw heightsync: emit request mode=%s nonce=%d", mode, wantNonce)
		return
	}
	if mode == "anchor" && composeHasHeightSyncEmit(t, ws, project, since, wantNonce, "request", "omit") {
		t.Fatalf("recover nonce %d: devshardctl heightsync request was omit (oracle SSE stale on devshardctl; host may show local_aligned>0)", wantNonce)
	}

	logQL := `{service_name="devshardctl"} |= "heightsync: emit"`
	lokiDeadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(lokiDeadline) {
		end := time.Now().Add(30 * time.Second)
		if waitLokiHeightSyncEmitMode(t, lokiClient, logQL, since, end, wantNonce, "request", mode, 5*time.Second) {
			t.Logf("loki devshardctl: saw heightsync: emit request mode=%s nonce=%d", mode, wantNonce)
			return
		}
		if composeHasHeightSyncEmit(t, ws, project, since.Add(-5*time.Second), wantNonce, "request", mode) {
			t.Logf("compose devshardctl: saw heightsync: emit request mode=%s nonce=%d (before Loki)", mode, wantNonce)
			return
		}
		time.Sleep(2 * time.Second)
	}

	auditInferenceStall(t, ws, project, lokiClient, wantNonce, since, time.Since(since))
	t.Fatalf(`missing devshardctl heightsync: emit request mode=%s for nonce %d (compose + Loki 90s)`, mode, wantNonce)
}
