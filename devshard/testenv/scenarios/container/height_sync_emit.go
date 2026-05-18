package container

import (
	"fmt"
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
		t.Fatalf("sync-turn nonce %d: devshardctl heightsync request was omit (courier peer-tip cache cold or mockdapi SSE stale; see heightsync: decide / peer_tip_cache)", wantNonce)
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

// countComposeHostResponseAnchors scans devshardd compose logs for outbound response anchors.
func countComposeHostResponseAnchors(t *testing.T, ws, project string, since time.Time, start, end int) int {
	t.Helper()
	seen := make(map[int]struct{})
	for i := 0; i < 4; i++ {
		svc := fmt.Sprintf("devshardd-testenv-%d", i)
		for _, ln := range composeLogsSince(t, ws, project, svc, since, 800) {
			kv := parseLogPayloadFromLine(ln)
			if kv["msg"] != "heightsync: emit" || kv["direction"] != "response" {
				continue
			}
			if !strings.EqualFold(kv["mode"], "anchor") {
				continue
			}
			n := parseNonce(kv["nonce"])
			if n >= start && n <= end {
				seen[n] = struct{}{}
			}
		}
	}
	return len(seen)
}

// countComposeHostResponseEmits counts distinct nonces with heightsync: emit response in [start,end].
// mode empty accepts any mode (anchor or omit).
func countComposeHostResponseEmits(t *testing.T, ws, project string, since time.Time, start, end int, mode string) int {
	t.Helper()
	seen := make(map[int]struct{})
	for i := 0; i < 4; i++ {
		svc := fmt.Sprintf("devshardd-testenv-%d", i)
		for _, ln := range composeLogsSince(t, ws, project, svc, since, 800) {
			kv := parseLogPayloadFromLine(ln)
			if kv["msg"] != "heightsync: emit" || kv["direction"] != "response" {
				continue
			}
			if mode != "" && !strings.EqualFold(kv["mode"], mode) {
				continue
			}
			n := parseNonce(kv["nonce"])
			if n >= start && n <= end {
				seen[n] = struct{}{}
			}
		}
	}
	return len(seen)
}

// composeCourierPeerTipCacheLatest returns the last devshardctl peer_tip_cache snapshot in logs.
func composeCourierPeerTipCacheLatest(t *testing.T, ws, project string, since time.Time) (cacheReady bool, verified, maxHeight int) {
	t.Helper()
	for _, ln := range composeLogsSince(t, ws, project, "devshardctl", since, 800) {
		kv := parseLogPayloadFromLine(ln)
		if kv["msg"] != "heightsync: peer_tip_cache" {
			continue
		}
		cacheReady = kv["cache_ready"] == "true"
		verified = parseNonce(kv["verified_origins"])
		maxHeight = parseNonce(kv["height"])
	}
	return cacheReady, verified, maxHeight
}

// composeCourierPeerTipCacheReady is true when the latest snapshot shows a warm verified cache.
func composeCourierPeerTipCacheReady(t *testing.T, ws, project string, since time.Time, minVerified int) (ready bool, verified, maxHeight int) {
	t.Helper()
	ready, verified, maxHeight = composeCourierPeerTipCacheLatest(t, ws, project, since)
	if ready && verified >= minVerified {
		return true, verified, maxHeight
	}
	return false, verified, maxHeight
}

func cheatingTrailInboundTrustOK(trust string) bool {
	switch strings.TrimSpace(trust) {
	case "peer_aligned", "untrusted_peer":
		return true
	default:
		return false
	}
}

// cheatingTrailPrefixesFromCompose scans host compose logs for peer vs response hash prefixes.
func cheatingTrailPrefixesFromCompose(t *testing.T, ws, project string, since time.Time, wantNonce int) (peerPrefix, respPrefix string) {
	t.Helper()
	for i := 0; i < 4; i++ {
		svc := fmt.Sprintf("devshardd-testenv-%d", i)
		for _, ln := range composeLogsSince(t, ws, project, svc, since, 800) {
			kv := parseLogPayloadFromLine(ln)
			switch kv["msg"] {
			case "heightsync: peer attestation received":
				if kv["direction"] != "request" || !strings.EqualFold(kv["mode"], "anchor") {
					continue
				}
				if parseNonce(kv["nonce"]) != wantNonce {
					continue
				}
				if !cheatingTrailInboundTrustOK(kv["trust_level"]) {
					continue
				}
				if p := strings.ToLower(strings.TrimSpace(kv["peer_block_hash_prefix"])); p != "" {
					peerPrefix = p
				}
			case "heightsync: emit":
				if kv["direction"] != "response" || !strings.EqualFold(kv["mode"], "anchor") {
					continue
				}
				if parseNonce(kv["nonce"]) != wantNonce {
					continue
				}
				if p := strings.ToLower(strings.TrimSpace(kv["block_hash_prefix"])); p != "" {
					respPrefix = p
				}
			}
		}
	}
	return peerPrefix, respPrefix
}
