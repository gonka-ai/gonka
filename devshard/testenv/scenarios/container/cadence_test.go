//go:build testenvci

package container

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func wantRequestAnchorAtNonce(nonce int) bool {
	switch {
	case nonce >= 1 && nonce <= 4:
		return true
	case nonce >= 8 && nonce <= 11:
		return true
	case nonce == 16:
		return true
	default:
		return false
	}
}

func TestContainerE2E_HeightSync_Cadence(t *testing.T) {
	if os.Getenv("TESTENV_SKIP_DOCKER_STACK") == "1" {
		t.Skip("TESTENV_SKIP_DOCKER_STACK=1")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}

	testenvDir := PrepareIsolatedE2EWorkspace(t, WithDevsharddSchedulerFromCopiedConfig())
	composeFile := filepath.Join(testenvDir, "docker-compose.yml")
	if _, err := os.Stat(composeFile); err != nil {
		t.Fatalf("workspace docker-compose.yml: %v", err)
	}

	project := ComposeProjectForTest(t)
	deadline, ok := t.Deadline()
	if !ok {
		deadline = time.Now().Add(20 * time.Minute)
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	down := func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer dcancel()
		_ = DockerCompose(dctx, testenvDir, project, nil, nil, "down", "--remove-orphans", "--timeout", "60").Run()
	}
	down()
	t.Cleanup(down)

	PruneStaleContainerE2EDockerStacks(t, testenvDir)

	up := DockerCompose(ctx, testenvDir, project, os.Stdout, os.Stderr, "up", "-d", "--build")
	if err := up.Run(); err != nil {
		t.Fatalf("docker compose up: %v", err)
	}

	LogComposeDebugHints(t, testenvDir, project)
	WaitCoreStackServicesRunningOrFail(t, ctx, testenvDir, project, time.Now().Add(4*time.Minute))

	httpClient := &http.Client{Timeout: 15 * time.Second}
	streamClient := &http.Client{Timeout: 5 * time.Minute}

	WaitHeightSyncPositive(t, httpClient, time.Now().Add(5*time.Minute))
	WaitHTTP_OK(t, httpClient, "http://127.0.0.1:8081/v1/status", time.Now().Add(4*time.Minute), "devshardctl /v1/status")
	WaitHTTP_OK(t, httpClient, "http://127.0.0.1:3100/ready", time.Now().Add(3*time.Minute), "loki")
	WaitHTTP_OK(t, httpClient, "http://127.0.0.1:8428/api/v1/query?query=1", time.Now().Add(3*time.Minute), "victoria-metrics")

	t0 := time.Now().Add(-30 * time.Second)

	const metricOutboundAnchors = "devshard_heightsync_outbound_anchors_total"
	baselineOutbound := PromInstantScalar(t, httpClient,
		fmt.Sprintf(`sum(%s{direction="response"})`, metricOutboundAnchors))

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
	for i := 1; i <= nInfer; i++ {
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
		}(i)
	}
	wg.Wait()
	if len(inferErrs) > 0 {
		t.Fatalf("inference errors: %v", errors.Join(inferErrs...))
	}

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
		ctlLines = LokiQueryRange(t, httpClient, logQLCtl, t0, end, 5000)
		hostLines = LokiQueryRange(t, httpClient, logQLHost, t0, end, 5000)
		a := CountHeightSyncEmitModes(ctlLines, "request", "anchor")
		o := CountHeightSyncEmitModes(ctlLines, "request", "omit")
		if a >= 9 && o >= 7 {
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

	anchor := CountHeightSyncEmitModes(ctlLines, "request", "anchor")
	omit := CountHeightSyncEmitModes(ctlLines, "request", "omit")
	if anchor != 9 {
		t.Fatalf("Loki devshardctl request anchor emits: got %d want 9", anchor)
	}
	if omit != 7 {
		t.Fatalf("Loki devshardctl request omit emits: got %d want 7", omit)
	}

	assertPrefixParity(t, ctlLines, hostLines)

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

func postChatCompletionStream(t *testing.T, ctx context.Context, c *http.Client, inferIdx int) error {
	t.Helper()
	body := []byte(`{"model":"llama","stream":true,"max_tokens":50}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8081/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Logf("inference %d: POST devshardctl: %v", inferIdx, err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		msg := strings.TrimSpace(truncateForLog(buf.String(), 800))
		t.Logf("inference %d: non-OK %d body=%q", inferIdx, resp.StatusCode, msg)
		return fmt.Errorf("http %d: %s", resp.StatusCode, buf.String())
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	const tailN = 12
	tail := make([]string, 0, tailN)
	for sc.Scan() {
		line := sc.Text()
		if len(tail) >= tailN {
			tail = tail[1:]
		}
		tail = append(tail, truncateForLog(line, 400))
		if strings.Contains(line, "[DONE]") {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		t.Logf("inference %d: SSE scanner error: %v; last lines=%q", inferIdx, err, tail)
		return fmt.Errorf("read SSE inference %d: %w", inferIdx, err)
	}
	t.Logf("inference %d: EOF without [DONE]; last SSE lines=%q", inferIdx, tail)
	return fmt.Errorf("inference %d: stream ended without [DONE]", inferIdx)
}

func truncateForLog(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func assertPrefixParity(t *testing.T, ctlLines, hostLines []string) {
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

	for nonce := 1; nonce <= 16; nonce++ {
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

func parseNonce(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	u, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0
	}
	return int(u)
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
