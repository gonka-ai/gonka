//go:build testenvci

package container

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestContainerE2E_HeightSync_CheatingTrail covers CONTAINER_E2E_PLAN §5.6 / §7.2 Phase B:
// POST /v1/debug/cheat-anchor?nonce=N arms a one-shot bogus mainnet hash for sync-turn lead N;
// Loki must show an inbound peer Anchor attestation while the host's response Anchor prefix
// (oracle-backed) differs from the peer's bogus prefix.
func TestContainerE2E_HeightSync_CheatingTrail(t *testing.T) {
	_, _, httpClient, streamClient, _ := startHeightSyncContainerStack(t)
	ctx := context.Background()
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}

	last := fetchDevshardctlLastNonce(t, httpClient)
	cheatNonce := nextSyncTurnLeadAfter(last)
	t.Logf("cheating trail: last=%d sync-turn lead nonce %d", last, cheatNonce)
	advanceSessionToNonce(t, ctx, streamClient, cheatNonce)

	cheatURL := fmt.Sprintf("http://127.0.0.1:8081/v1/debug/cheat-anchor?nonce=%d", cheatNonce)
	cheatReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cheatURL, nil)
	if err != nil {
		t.Fatalf("new cheat request: %v", err)
	}
	cheatResp, err := httpClient.Do(cheatReq)
	if err != nil {
		t.Fatalf("POST cheat-anchor: %v", err)
	}
	cheatResp.Body.Close()
	if cheatResp.StatusCode != http.StatusNoContent {
		t.Fatalf("cheat-anchor: status %d (want %d); ensure compose sets DEVSHARDCTL_DEBUG=1 on devshardctl",
			cheatResp.StatusCode, http.StatusNoContent)
	}

	if err := postChatCompletionStream(t, ctx, streamClient, int(cheatNonce)); err != nil {
		t.Fatalf("nonce %d: %v", cheatNonce, err)
	}

	lokiStart := LokiWindowStart()
	logQLPeer := `{service_name="devshardd-testenv"} |= "heightsync: peer attestation received"`
	logQLResp := `{service_name="devshardd-testenv"} |= "heightsync: emit"`
	deadline := time.Now().Add(3 * time.Minute)
	var peerPrefix, respPrefix string
	var lastStatusLog time.Time
	wantNonce := int(cheatNonce)
	for time.Now().Before(deadline) {
		end := time.Now().Add(30 * time.Second)
		for _, ln := range LokiQueryRange(t, httpClient, logQLPeer, lokiStart, end, 5000) {
			kv := ParseLogKV(ln)
			if kv["msg"] != "heightsync: peer attestation received" || kv["direction"] != "request" {
				continue
			}
			if !strings.EqualFold(kv["mode"], "anchor") {
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
		}
		for _, ln := range LokiQueryRange(t, httpClient, logQLResp, lokiStart, end, 5000) {
			kv := ParseLogKV(ln)
			if kv["msg"] != "heightsync: emit" || kv["direction"] != "response" {
				continue
			}
			if !strings.EqualFold(kv["mode"], "anchor") {
				continue
			}
			if parseNonce(kv["nonce"]) != wantNonce {
				continue
			}
			if p := strings.ToLower(strings.TrimSpace(kv["block_hash_prefix"])); p != "" {
				respPrefix = p
			}
		}
		if peerPrefix != "" && respPrefix != "" && peerPrefix != respPrefix {
			return
		}
		now := time.Now()
		if now.Sub(lastStatusLog) > 20*time.Second {
			lastStatusLog = now
			t.Logf("loki wait nonce=%d: peer_prefix=%q resp_prefix=%q (want both non-empty and different)", wantNonce, peerPrefix, respPrefix)
		}
		time.Sleep(2 * time.Second)
	}
	if peerPrefix == "" {
		logCheatingTrailLokiDebug(t, httpClient, logQLPeer, lokiStart, wantNonce)
		t.Fatalf(`Loki: missing host "peer attestation received" for nonce=%d (mode=anchor, trust peer_aligned|untrusted_peer) with peer_block_hash_prefix`, wantNonce)
	}
	if respPrefix == "" {
		t.Fatalf(`Loki: missing host "heightsync: emit" response anchor for nonce=%d with block_hash_prefix`, wantNonce)
	}
	if peerPrefix == respPrefix {
		t.Fatalf("expected peer bogus prefix %q to differ from oracle response prefix %q (cheat-anchor for nonce %d)", peerPrefix, respPrefix, wantNonce)
	}
}

func cheatingTrailInboundTrustOK(trust string) bool {
	switch strings.TrimSpace(trust) {
	case "peer_aligned", "untrusted_peer":
		return true
	default:
		return false
	}
}

func logCheatingTrailLokiDebug(t *testing.T, c *http.Client, logQL string, t0 time.Time, wantNonce int) {
	t.Helper()
	end := time.Now().Add(30 * time.Second)
	lines := LokiQueryRange(t, c, logQL, t0, end, 200)
	const maxSamples = 8
	n := 0
	for _, ln := range lines {
		kv := ParseLogKV(ln)
		if kv["msg"] != "heightsync: peer attestation received" || parseNonce(kv["nonce"]) != wantNonce {
			continue
		}
		t.Logf("loki sample nonce=%d: mode=%q trust_level=%q peer_block_hash_prefix=%q",
			wantNonce, kv["mode"], kv["trust_level"], kv["peer_block_hash_prefix"])
		n++
		if n >= maxSamples {
			break
		}
	}
	if n == 0 {
		t.Logf("loki: no parsed nonce=%d peer-attestation lines in %d raw matches", wantNonce, len(lines))
	}
}
