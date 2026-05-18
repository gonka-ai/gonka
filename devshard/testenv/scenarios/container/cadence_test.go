//go:build testenvci

package container

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestContainerE2E_HeightSync_Cadence(t *testing.T) {
	ws, project, httpClient, streamClient, _ := startHeightSyncContainerStack(t)
	ctx := context.Background()
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}

	const metricOutboundAnchors = "devshard_heightsync_outbound_anchors_total"
	baselineOutbound := PromInstantScalar(t, httpClient,
		fmt.Sprintf(`sum(%s{direction="response"})`, metricOutboundAnchors))

	lead := nextSyncTurnLeadNonce(fetchDevshardctlNextNonce(t, httpClient))
	advanceSessionToNonce(t, ctx, streamClient, lead)
	assertHostsEscrowAlignedWithCourier(t, httpClient)

	const nInfer = 16
	startNonce := int(lead)
	endNonce := startNonce + nInfer - 1
	t.Logf("cadence window: sync-turn lead=%d nonces %d..%d", lead, startNonce, endNonce)

	if ctx.Err() != nil {
		t.Fatalf("deadline before inference window: %v", ctx.Err())
	}

	lokiStart := time.Now().Add(-5 * time.Second)
	runCadenceProductionWaves(t, ctx, streamClient, startNonce, &CadenceWarmOpts{
		Ws: ws, Project: project, Loki: httpClient, LokiStart: lokiStart,
	})
	wavesDoneAt := time.Now()

	logQLCtl := `{service_name="devshardctl"} |= "heightsync: emit"`
	logQLHost := `{service_name="devshardd-testenv"} |= "heightsync: peer attestation received"`
	logQLHostEmit := `{service_name="devshardd-testenv"} |= "heightsync: emit"`

	deadlinePoll := time.Now().Add(90 * time.Second)
	var ctlLines, hostLines, hostEmitLines []string
	seenAnchorNonce := make(map[int]struct{})
	seenOmitNonce := make(map[int]struct{})
	lastA, lastO := -1, -1
	var lastStatusLog time.Time
	for time.Now().Before(deadlinePoll) {
		end := time.Now().Add(30 * time.Second)
		ctlLines = LokiQueryRange(t, httpClient, logQLCtl, lokiStart, end, 5000)
		hostLines = LokiQueryRange(t, httpClient, logQLHost, lokiStart, end, 5000)
		hostEmitLines = LokiQueryRange(t, httpClient, logQLHostEmit, lokiStart, end, 5000)
		a := CountHeightSyncRequestEmitInRange(ctlLines, startNonce, endNonce, "anchor")
		o := CountHeightSyncRequestEmitInRange(ctlLines, startNonce, endNonce, "omit")
		if cadenceCourierLokiReady(t, ws, project, lokiStart, ctlLines, startNonce, endNonce, a, o) {
			break
		}
		if reason := cadenceCourierLokiGiveUp(t, ws, project, lokiStart, hostEmitLines, startNonce, endNonce, a, o, wavesDoneAt); reason != "" {
			t.Fatalf("loki: %s (saw_receipt=false on SSE is normal — devshardctl does not forward receipt lines to the test client)", reason)
		}
		for _, n := range distinctEmitNonces(ctlLines, "anchor") {
			if _, ok := seenAnchorNonce[n]; ok {
				continue
			}
			seenAnchorNonce[n] = struct{}{}
			t.Logf("loki: first heightsync anchor emit for nonce=%d (counts anchor=%d omit=%d)", n, a, o)
		}
		for _, n := range distinctEmitNonces(ctlLines, "omit") {
			if _, ok := seenOmitNonce[n]; ok {
				continue
			}
			seenOmitNonce[n] = struct{}{}
			t.Logf("loki: first heightsync omit emit for nonce=%d (counts anchor=%d omit=%d)", n, a, o)
		}
		now := time.Now()
		if a != lastA || o != lastO || now.Sub(lastStatusLog) > 25*time.Second {
			lastA, lastO = a, o
			lastStatusLog = now
			hostResp := CountHeightSyncEmitInRange(hostEmitLines, startNonce, endNonce, "response", "anchor")
			t.Logf("loki wait: request anchor=%d omit=%d host_response_anchor=%d (want periodic sync-turn request anchor, e.g. nonce 8; initial cold omits OK)",
				a, o, hostResp)
		}
		time.Sleep(3 * time.Second)
	}

	if !cadenceCourierLokiReady(t, ws, project, lokiStart, ctlLines, startNonce, endNonce,
		CountHeightSyncRequestEmitInRange(ctlLines, startNonce, endNonce, "anchor"),
		CountHeightSyncRequestEmitInRange(ctlLines, startNonce, endNonce, "omit")) {
		t.Fatalf("loki: timed out waiting for courier cadence emits (90s); last request anchor=%d omit=%d",
			CountHeightSyncRequestEmitInRange(ctlLines, startNonce, endNonce, "anchor"),
			CountHeightSyncRequestEmitInRange(ctlLines, startNonce, endNonce, "omit"))
	}

	assertCadenceRequestEmits(t, ctlLines, startNonce, endNonce)

	assertPrefixParity(t, ctlLines, hostLines, startNonce, endNonce)

	// Host-exported Prometheus counters (response-direction outbound anchors), summed across replicas.
	promDeadline := time.Now().Add(3 * time.Minute)
	var delta float64
	for time.Now().Before(promDeadline) {
		after := PromInstantScalar(t, httpClient,
			fmt.Sprintf(`sum(%s{direction="response"})`, metricOutboundAnchors))
		delta = after - baselineOutbound
		if delta >= 8.5 {
			break
		}
		t.Logf("prom wait: Δ outbound response anchors=%g (want >= 8.5)", delta)
		time.Sleep(5 * time.Second)
	}
	if delta < 8.5 {
		t.Fatalf("Prometheus outbound response anchors delta: got %g want >= 8.5 (baseline=%g)", delta, baselineOutbound)
	}
}

// cadenceCourierLokiReady returns true once courier-mode emits are present: cold initial
// sync-turn omits (when in window), at least one warmed periodic sync-turn anchor, and
// between-turn traffic.
func cadenceCourierLokiReady(t *testing.T, ws, project string, since time.Time, ctlLines []string, start, end, anchors, omits int) bool {
	t.Helper()
	if anchors < 4 || omits < 4 {
		return false
	}
	if n, ok := firstPeriodicSyncTurnInWindow(start, end); ok {
		if HasHeightSyncRequestEmit(ctlLines, n, "anchor") {
			return true
		}
		if composeHasHeightSyncEmit(t, ws, project, since, n, "request", "anchor") {
			t.Logf("loki: periodic sync-turn anchor for nonce=%d visible in compose before Loki", n)
			return true
		}
	}
	return anchors >= 4
}

// cadenceCourierLokiGiveUp detects a stuck courier cache (all request omits, no anchors)
// so the test fails in ~45s instead of polling for the full deadline.
func cadenceCourierLokiGiveUp(t *testing.T, ws, project string, since time.Time, hostEmitLines []string, start, end, anchors, omits int, wavesDoneAt time.Time) string {
	window := end - start + 1
	if anchors > 0 || omits < window {
		return ""
	}
	if time.Since(wavesDoneAt) < 45*time.Second {
		return ""
	}
	hostResp := CountHeightSyncEmitInRange(hostEmitLines, start, end, "response", "anchor")
	hostRespCompose := countComposeHostResponseAnchors(t, ws, project, since, start, end)
	if hostRespCompose > hostResp {
		hostResp = hostRespCompose
	}
	if hostResp >= 4 {
		return fmt.Sprintf(
			"courier never emitted request anchors (all %d nonces omit) but hosts logged %d response anchors — peer-tip ingest likely failed (check devshardctl for heightsync: origin_sig_invalid; RequireVerifiedBlob)",
			window, hostResp)
	}
	if n, ok := firstPeriodicSyncTurnInWindow(start, end); ok {
		if composeHasHeightSyncEmit(t, ws, project, since, n, "request", "anchor") {
			return ""
		}
	}
	return fmt.Sprintf(
		"courier never emitted request anchors after %s (all %d nonces omit; host response anchors: Loki=%d compose=%d — if both 0, hosts never anchored; wave-1 should be sequential for courier)",
		time.Since(wavesDoneAt).Round(time.Second), window,
		CountHeightSyncEmitInRange(hostEmitLines, start, end, "response", "anchor"), hostRespCompose)
}

// assertCadenceRequestEmits checks courier devshardctl cadence: cold initial sync-turn
// (nonces 1..slots) may Omit until host responses warm peer tips; later sync-turn slots
// must Anchor; between-turn slots may Omit or lazy Anchor.
func assertCadenceRequestEmits(t *testing.T, ctlLines []string, startNonce, endNonce int) {
	t.Helper()
	for n := startNonce; n <= endNonce; n++ {
		if wantRequestAnchorAtNonce(n) {
			if isInitialCourierSyncTurn(n) && n >= startNonce && n <= endNonce {
				if !HasHeightSyncRequestEmit(ctlLines, n, "omit") && !HasHeightSyncRequestEmit(ctlLines, n, "anchor") {
					t.Fatalf("cadence: initial sync-turn nonce %d missing request emit (cold courier omit or warm anchor)", n)
				}
				continue
			}
			if !HasHeightSyncRequestEmit(ctlLines, n, "anchor") {
				t.Fatalf("cadence: sync-turn nonce %d missing request mode=anchor", n)
			}
			continue
		}
		if !HasHeightSyncRequestEmit(ctlLines, n, "omit") && !HasHeightSyncRequestEmit(ctlLines, n, "anchor") {
			t.Fatalf("cadence: between-turn nonce %d missing request emit (omit or lazy anchor)", n)
		}
	}
	omit := CountHeightSyncRequestEmitInRange(ctlLines, startNonce, endNonce, "omit")
	if omit < 4 {
		t.Fatalf("cadence: distinct omit emits in window: got %d want at least 4 (cold initial sync-turn + between-turn)", omit)
	}
	anchor := CountHeightSyncRequestEmitInRange(ctlLines, startNonce, endNonce, "anchor")
	if anchor < 4 {
		t.Fatalf("cadence: distinct anchor emits in window: got %d want at least 4 (periodic sync-turn + lazy carry)", anchor)
	}
}

func assertPrefixParity(t *testing.T, ctlLines, hostLines []string, startNonce, endNonce int) {
	t.Helper()
	type prefixRec struct {
		userPrefix string
		hostPrefix string
	}
	byNonce := make(map[int]prefixRec)

	for _, ln := range ctlLines {
		kv := ParseLogKV(ln)
		if kv["msg"] != "heightsync: emit" || kv["direction"] != "request" {
			continue
		}
		if !strings.EqualFold(kv["mode"], "anchor") {
			continue
		}
		n := parseNonce(kv["nonce"])
		if n == 0 {
			continue
		}
		rec := byNonce[n]
		rec.userPrefix = strings.ToLower(strings.TrimSpace(kv["block_hash_prefix"]))
		byNonce[n] = rec
	}

	for _, ln := range hostLines {
		kv := ParseLogKV(ln)
		if kv["msg"] != "heightsync: peer attestation received" || kv["direction"] != "request" {
			continue
		}
		if !strings.EqualFold(kv["mode"], "anchor") {
			continue
		}
		n := parseNonce(kv["nonce"])
		if n == 0 {
			continue
		}
		rec := byNonce[n]
		rec.hostPrefix = strings.ToLower(strings.TrimSpace(kv["peer_block_hash_prefix"]))
		byNonce[n] = rec
	}

	for nonce := startNonce; nonce <= endNonce; nonce++ {
		if !wantRequestAnchorAtNonce(nonce) {
			continue
		}
		if isInitialCourierSyncTurn(nonce) && !HasHeightSyncRequestEmit(ctlLines, nonce, "anchor") {
			continue // cold courier: omit on initial sync turn — no user anchor prefix to match
		}
		rec, ok := byNonce[nonce]
		if !ok || rec.userPrefix == "" || rec.hostPrefix == "" {
			t.Fatalf("prefix parity: missing log lines for anchored nonce=%d (user=%q host=%q)", nonce, rec.userPrefix, rec.hostPrefix)
		}
		if rec.userPrefix != rec.hostPrefix {
			t.Fatalf("prefix parity mismatch nonce=%d user=%q host=%q", nonce, rec.userPrefix, rec.hostPrefix)
		}
	}
}

// distinctEmitNonces returns sorted distinct nonce values from heightsync: emit lines (request + mode).
func distinctEmitNonces(lines []string, mode string) []int {
	seen := make(map[int]struct{})
	for _, ln := range lines {
		kv := ParseLogKV(ln)
		if kv["msg"] != "heightsync: emit" || kv["direction"] != "request" {
			continue
		}
		if !strings.EqualFold(kv["mode"], mode) {
			continue
		}
		n := parseNonce(kv["nonce"])
		if n > 0 {
			seen[n] = struct{}{}
		}
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

