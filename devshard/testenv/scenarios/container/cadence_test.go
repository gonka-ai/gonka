//go:build testenvci

package container

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestContainerE2E_HeightSync_Cadence(t *testing.T) {
	_, _, httpClient, streamClient, _ := startHeightSyncContainerStack(t)
	ctx := context.Background()
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}

	const metricOutboundAnchors = "devshard_heightsync_outbound_anchors_total"
	baselineOutbound := PromInstantScalar(t, httpClient,
		fmt.Sprintf(`sum(%s{direction="response"})`, metricOutboundAnchors))

	startNonce := int(fetchDevshardctlNextNonce(t, httpClient))
	t.Logf("cadence burst: nonces %d..%d from /v1/status", startNonce, startNonce+15)

	// Fire all chat-completions in parallel: do not wait for one inference stream to finish
	// before starting the next. Completion is async on devshardctl/hosts; we only wait for
	// the HTTP+SSE readers in these goroutines after all requests are in flight.
	if ctx.Err() != nil {
		t.Fatalf("deadline before inference burst: %v", ctx.Err())
	}
	const nInfer = 16
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var inferErrs []error
	var inferMu sync.Mutex
	completedOK := 0
	for i := 0; i < nInfer; i++ {
		inferNonce := startNonce + i
		wg.Add(1)
		go func(inferIdx int) {
			defer wg.Done()
			if err := postChatCompletionStream(t, ctx, streamClient, inferIdx); err != nil {
				errMu.Lock()
				inferErrs = append(inferErrs, fmt.Errorf("inference %d: %w", inferIdx, err))
				errMu.Unlock()
				return
			}
			inferMu.Lock()
			completedOK++
			done := completedOK
			inferMu.Unlock()
			t.Logf("inference nonce=%d finished — burst %d/%d (%d%%)", inferIdx, done, nInfer, (100*done)/nInfer)
		}(inferNonce)
	}
	wg.Wait()
	if len(inferErrs) > 0 {
		t.Fatalf("inference errors: %v", errors.Join(inferErrs...))
	}

	lokiStart := LokiWindowStart()
	logQLCtl := `{service_name="devshardctl"} |= "heightsync: emit"`
	logQLHost := `{service_name="devshardd-testenv"} |= "heightsync: peer attestation received"`

	deadlinePoll := time.Now().Add(3 * time.Minute)
	var ctlLines, hostLines []string
	seenAnchorNonce := make(map[int]struct{})
	seenOmitNonce := make(map[int]struct{})
	lastA, lastO := -1, -1
	var lastStatusLog time.Time
	for time.Now().Before(deadlinePoll) {
		end := time.Now().Add(30 * time.Second)
		ctlLines = LokiQueryRange(t, httpClient, logQLCtl, lokiStart, end, 5000)
		hostLines = LokiQueryRange(t, httpClient, logQLHost, lokiStart, end, 5000)
		a := CountHeightSyncRequestEmitInRange(ctlLines, startNonce, startNonce+nInfer-1, "anchor")
		o := CountHeightSyncRequestEmitInRange(ctlLines, startNonce, startNonce+nInfer-1, "omit")
		if a >= 9 && o >= 6 {
			break
		}
		for _, n := range distinctEmitNonces(ctlLines, "anchor") {
			if _, ok := seenAnchorNonce[n]; ok {
				continue
			}
			seenAnchorNonce[n] = struct{}{}
			t.Logf("loki: first heightsync anchor emit for nonce=%d (counts anchor=%d/9 omit=%d/7)", n, a, o)
		}
		for _, n := range distinctEmitNonces(ctlLines, "omit") {
			if _, ok := seenOmitNonce[n]; ok {
				continue
			}
			seenOmitNonce[n] = struct{}{}
			t.Logf("loki: first heightsync omit emit for nonce=%d (counts anchor=%d/9 omit=%d/7)", n, a, o)
		}
		now := time.Now()
		if a != lastA || o != lastO || now.Sub(lastStatusLog) > 25*time.Second {
			lastA, lastO = a, o
			lastStatusLog = now
			pct := lokiThresholdPercent(a, o)
			t.Logf("loki wait: anchor=%d/9 omit=%d/7 (~%d%% toward thresholds; distinct anchor_nonces=%d omit_nonces=%d)",
				a, o, pct, len(seenAnchorNonce), len(seenOmitNonce))
		}
		time.Sleep(3 * time.Second)
	}

	assertCadenceRequestEmits(t, ctlLines, startNonce, startNonce+nInfer-1)

	assertPrefixParity(t, ctlLines, hostLines, startNonce, startNonce+nInfer-1)

	// Host-exported Prometheus counters (response-direction outbound anchors), summed across replicas.
	promDeadline := time.Now().Add(3 * time.Minute)
	var delta float64
	for time.Now().Before(promDeadline) {
		after := PromInstantScalar(t, httpClient,
			fmt.Sprintf(`sum(%s{direction="response"})`, metricOutboundAnchors))
		delta = after - baselineOutbound
		if delta >= 8.5 && delta <= 9.5 {
			break
		}
		t.Logf("prom wait: Δ outbound response anchors=%g (want ~9)", delta)
		time.Sleep(5 * time.Second)
	}
	if delta < 8.5 || delta > 9.5 {
		t.Fatalf("Prometheus outbound response anchors delta: got %g want ~9 (baseline=%g)", delta, baselineOutbound)
	}
}

// assertCadenceRequestEmits checks sync-turn slots emit Anchor; between-turn slots may Omit
// or Anchor (v2 lazy carry-forward). Total anchor count may exceed 9 when lazy fires.
func assertCadenceRequestEmits(t *testing.T, ctlLines []string, startNonce, endNonce int) {
	t.Helper()
	for n := startNonce; n <= endNonce; n++ {
		if wantRequestAnchorAtNonce(n) {
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
	if omit < 6 {
		t.Fatalf("cadence: distinct omit emits in window: got %d want at least 6 (lazy carry may replace some)", omit)
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

const wantAnchorEmits = 9
const wantOmitEmits = 7

// lokiThresholdPercent is a conservative combined progress estimate toward both gates.
func lokiThresholdPercent(anchor, omit int) int {
	pa := 100 * anchor / wantAnchorEmits
	if pa > 100 {
		pa = 100
	}
	po := 100 * omit / wantOmitEmits
	if po > 100 {
		po = 100
	}
	if pa < po {
		return pa
	}
	return po
}
