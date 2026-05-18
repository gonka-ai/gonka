package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	heightSyncAnchorK    = 8
	heightSyncSyncSlots  = 4
	devshardctlStatusURL = "http://127.0.0.1:8081/v1/status"
	// mockdapiStaleAfter matches MOCKDAPI_STALE_AFTER from gencompose (block_time + jitter + 1s, min 10s).
	mockdapiStaleAfter = 10 * time.Second
)

type devshardctlStatus struct {
	Nonce uint64 `json:"nonce"`
}

// fetchDevshardctlLastNonce returns the last applied inference nonce from GET /v1/status.
// The next PrepareInference uses last+1.
func fetchDevshardctlLastNonce(t *testing.T, c *http.Client) uint64 {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, devshardctlStatusURL, nil)
	if err != nil {
		t.Fatalf("status request: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/status: http %d", resp.StatusCode)
	}
	var st devshardctlStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("decode /v1/status: %v", err)
	}
	return st.Nonce
}

// fetchDevshardctlNextNonce is the nonce the next chat-completions call will assign.
func fetchDevshardctlNextNonce(t *testing.T, c *http.Client) uint64 {
	t.Helper()
	return fetchDevshardctlLastNonce(t, c) + 1
}

// heightSyncInSyncTurn matches heightsync.AnchorScheduler cadence (K=8, slots=4).
func heightSyncInSyncTurn(n uint64) bool {
	if n == 0 {
		return false
	}
	if n <= heightSyncSyncSlots {
		return true
	}
	return n >= heightSyncAnchorK && n%heightSyncAnchorK < heightSyncSyncSlots
}

func wantRequestAnchorAtNonce(nonce int) bool {
	return heightSyncInSyncTurn(uint64(nonce))
}

// isInitialCourierSyncTurn is the global first sync-turn window (nonces 1..slots).
// devshardctl is courier-only (PeerTipOracleSource): a cold cache forces Omit on
// these nonces even under cadence (see heightsync_anchor_e2e_courier_test.go).
func isInitialCourierSyncTurn(nonce int) bool {
	return uint64(nonce) >= 1 && uint64(nonce) <= heightSyncSyncSlots
}

// firstPeriodicSyncTurnInWindow is the first cadence Anchor slot in [start,end]
// that is not the global initial sync turn (e.g. nonce 8 when start=1).
func firstPeriodicSyncTurnInWindow(start, end int) (int, bool) {
	for n := start; n <= end; n++ {
		if wantRequestAnchorAtNonce(n) && !isInitialCourierSyncTurn(n) {
			return n, true
		}
	}
	return 0, false
}

// hostServiceForNonce maps round-robin executor slot to compose service name.
func hostServiceForNonce(n uint64) string {
	return fmt.Sprintf("devshardd-testenv-%d", n%4)
}

// isSyncTurnLead is true at the first nonce of each sync-turn window: initial 1, then
// periodic starts 8, 16, 24, … (nonce % K == 0). These are treated like absolute nonce 1
// in the lost-first-response scenario.
func isSyncTurnLead(n uint64) bool {
	if n == 1 {
		return true
	}
	return n >= heightSyncAnchorK && n%heightSyncAnchorK == 0
}

// nextSyncTurnLeadNonce returns the next sync-turn lead at or after from.
func nextSyncTurnLeadNonce(from uint64) uint64 {
	if from <= 1 {
		return 1
	}
	if isSyncTurnLead(from) {
		return from
	}
	if from < heightSyncAnchorK {
		return heightSyncAnchorK
	}
	rem := from % heightSyncAnchorK
	return from + (heightSyncAnchorK - rem)
}

// waitMockdapiStaleQuietPeriod sleeps long enough for MOCKDAPI_STALE_AFTER after the
// height-sync feed stops (SSE quiet → client.Stale() → Omit on the next sync-turn nonce).
func waitMockdapiStaleQuietPeriod(t *testing.T) {
	t.Helper()
	time.Sleep(mockdapiStaleAfter + 2*time.Second)
}

// nextSyncTurnLeadAfter returns the next sync-turn lead strictly after lastApplied.
func nextSyncTurnLeadAfter(lastApplied uint64) uint64 {
	if lastApplied == 0 {
		return 1
	}
	return nextSyncTurnLeadNonce(lastApplied + 1)
}

// nextLostFirstResponseScenario picks the next sync-turn lead ≥ from and recover=lead+1,
// mirroring the in-process test at absolute nonces 1/2 on every sync-turn boundary.
func nextLostFirstResponseScenario(from uint64) (lead, recover uint64) {
	lead = nextSyncTurnLeadNonce(from)
	return lead, lead + 1
}

// nextForceAnchorTrigger is the next cadence-Omit nonce (7, 15, 23, …) suitable for force_height_sync_anchor.
func nextForceAnchorTrigger(from uint64) uint64 {
	rem := from % heightSyncAnchorK
	if rem == 7 {
		return from
	}
	if rem < 7 {
		return from + (7 - rem)
	}
	return from + (heightSyncAnchorK - rem + 7)
}

const devshardctlArmHostHoldURL = "http://127.0.0.1:8081/v1/debug/arm-host-hold"

// armHostInferenceHold blocks the next inference HTTP response on a host (before SSE/receipt).
// Requires devshardctl/devshardd built with -tags=dev and DEVSHARDCTL_DEBUG=1 / DEVSHARDD_DEBUG=1 (gencompose).
func armHostInferenceHold(t *testing.T, hostIdx int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	url := fmt.Sprintf("%s?host_idx=%d", devshardctlArmHostHoldURL, hostIdx)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		t.Fatalf("arm hold host_idx=%d: %v", hostIdx, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("arm hold host_idx=%d: %v", hostIdx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		t.Fatalf("arm hold host_idx=%d: status %d: %s (-tags=dev and DEVSHARDCTL_DEBUG=1 / DEVSHARDD_DEBUG=1?)",
			hostIdx, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	t.Logf("armed inference response hold on devshardd-testenv-%d", hostIdx)
}

// runParallelInferenceWave posts count chat-completions in parallel for distinct round-robin
// hosts within the wave, then waits for all to finish. Callers must not overlap waves on the
// same host (cadence waves are sized so each host gets at most one new nonce per wave).
func runParallelInferenceWave(t *testing.T, ctx context.Context, c *http.Client, startNonce, count int) {
	t.Helper()
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var inferErrs []error
	for i := 0; i < count; i++ {
		n := startNonce + i
		wg.Add(1)
		go func(inferNonce int) {
			defer wg.Done()
			if err := postChatCompletionStream(t, ctx, c, inferNonce); err != nil {
				errMu.Lock()
				inferErrs = append(inferErrs, fmt.Errorf("inference %d: %w", inferNonce, err))
				errMu.Unlock()
			}
		}(n)
	}
	wg.Wait()
	if len(inferErrs) > 0 {
		t.Fatalf("parallel wave nonces %d..%d: %v", startNonce, startNonce+count-1, errors.Join(inferErrs...))
	}
	t.Logf("parallel wave done: nonces %d..%d (%d in flight)", startNonce, startNonce+count-1, count)
}

// CadenceWarmOpts hooks compose/Loki for waiting on courier peer-tip cache after wave 1.
type CadenceWarmOpts struct {
	Ws, Project string
	Loki        *http.Client
	LokiStart   time.Time
}

// warmCourierPeerTipsAtNonce runs one inference at warmNonce (omit window before a periodic
// sync-turn lead) and waits until devshardctl ingests verified host response anchors. Use after
// devshardctl restart clears the in-memory peer-tip cache.
func warmCourierPeerTipsAtNonce(
	t *testing.T,
	ctx context.Context,
	streamClient *http.Client,
	ws, project string,
	httpClient *http.Client,
	warmNonce uint64,
) {
	t.Helper()
	if warmNonce == 0 {
		return
	}
	advanceSessionToNonce(t, ctx, streamClient, warmNonce)
	warmSince := time.Now().Add(-15 * time.Second)
	if err := postChatCompletionStream(t, ctx, streamClient, int(warmNonce)); err != nil {
		t.Fatalf("warm peer tips nonce %d: %v", warmNonce, err)
	}
	waitCourierPeerTipsAfterInitialWave(t, &CadenceWarmOpts{
		Ws: ws, Project: project, Loki: httpClient, LokiStart: warmSince,
	}, int(warmNonce), int(warmNonce))
}

// waitCourierPeerTipsAfterInitialWave blocks until devshardctl's courier peer-tip cache is
// ready (verified origin blobs ingested; MaxFresh non-nil). Host response omit on the cold
// initial sync turn does not warm the cache — grep heightsync: peer_tip_cache and
// heightsync: origin_sig_invalid on devshardctl while waiting.
func waitCourierPeerTipsAfterInitialWave(t *testing.T, warm *CadenceWarmOpts, waveStart, waveEnd int) {
	t.Helper()
	if warm == nil {
		return
	}
	const minVerifiedOrigins = 1 // MaxFresh needs one fresh verified originator tip
	_ = waveStart
	_ = waveEnd
	deadline := time.Now().Add(90 * time.Second)
	lastLog := time.Time{}
	var lastVerified, lastHeight int
	for time.Now().Before(deadline) {
		if ready, verified, maxH := composeCourierPeerTipCacheReady(t, warm.Ws, warm.Project, warm.LokiStart, minVerifiedOrigins); ready {
			t.Logf("courier warm: peer-tip cache ready (verified_origins=%d max_height=%d after nonces %d..%d)",
				verified, maxH, waveStart, waveEnd)
			return
		}
		now := time.Now()
		if now.Sub(lastLog) >= 8*time.Second {
			lastLog = now
			_, lastVerified, lastHeight = composeCourierPeerTipCacheLatest(t, warm.Ws, warm.Project, warm.LokiStart)
			anchorCompose := countComposeHostResponseAnchors(t, warm.Ws, warm.Project, warm.LokiStart, waveStart, waveEnd)
			t.Logf("courier warm wait: cache_ready=false verified_origins=%d max_height=%d host_response_anchor=%d (want cache_ready=true verified_origins>=%d — see devshardctl heightsync: peer_tip_cache)",
				lastVerified, lastHeight, anchorCompose, minVerifiedOrigins)
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("courier peer-tip cache not ready after nonces %d..%d (last verified_origins=%d max_height=%d; need cache_ready with verified_origins>=%d — check devshardctl for peer_tip_cache / origin_sig_invalid / request_decide_omit)",
		waveStart, waveEnd, lastVerified, lastHeight, minVerifiedOrigins)
}

// runCadenceProductionWaves drives one K=8/slots=4 cadence window from sync-turn lead:
// sync-turn (4 parallel) → [wait for courier cache] → omit (3 parallel) → …
func runCadenceProductionWaves(t *testing.T, ctx context.Context, c *http.Client, lead int, warm *CadenceWarmOpts) {
	t.Helper()
	waves := []struct {
		label string
		off   int
		n     int
	}{
		{"sync-turn-1", 0, 4},
		{"omit-1", 4, 3},
		{"sync-turn-2", 7, 4},
		{"omit-2", 11, 4},
		{"periodic-anchor", 15, 1},
	}
	for _, w := range waves {
		start := lead + w.off
		if w.label == "sync-turn-1" && isInitialCourierSyncTurn(lead) {
			// Cold courier: sync-turn 1..4 are request omit; cache warms from verified host
			// response anchors on between-turn traffic (omit-1), not from response omit.
			t.Logf("cadence wave %s: nonces %d..%d sequential (cold courier)", w.label, start, start+w.n-1)
			runSequentialInferenceWindow(t, ctx, c, start, w.n)
			continue
		}
		t.Logf("cadence wave %s: nonces %d..%d parallel", w.label, start, start+w.n-1)
		runParallelInferenceWave(t, ctx, c, start, w.n)
		if w.label == "omit-1" && isInitialCourierSyncTurn(lead) && warm != nil {
			waitCourierPeerTipsAfterInitialWave(t, warm, lead, start+w.n-1)
		}
	}
}

// runSequentialInferenceWindow posts count consecutive chat-completions starting at startNonce.
// Each inference completes before the next starts so devshardctl and host escrow nonces stay aligned.
func runSequentialInferenceWindow(t *testing.T, ctx context.Context, c *http.Client, startNonce, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		n := startNonce + i
		if err := postChatCompletionStream(t, ctx, c, n); err != nil {
			t.Fatalf("inference %d: %v", n, err)
		}
		t.Logf("inference nonce=%d finished — %d/%d", n, i+1, count)
	}
}

// advanceSessionToNonce runs chat-completions until the next inference will use nonce target
// (i.e. /v1/status last-applied nonce is target-1). Inferences may still be executing on
// hosts after Confirm; only session nonce alignment matters.
func advanceSessionToNonce(t *testing.T, ctx context.Context, c *http.Client, target uint64) {
	t.Helper()
	for {
		last := fetchDevshardctlLastNonce(t, c)
		next := last + 1
		if next == target {
			t.Logf("session ready: last=%d next inference nonce=%d", last, target)
			return
		}
		if next > target {
			t.Fatalf("session next nonce %d already past target %d (last=%d)", next, target, last)
		}
		stepStart := time.Now()
		t.Logf("advance: inference nonce %d → target %d", next, target)
		if err := postChatCompletionStream(t, ctx, c, int(next)); err != nil {
			t.Fatalf("advance inference %d: %v", next, err)
		}
		if step := time.Since(stepStart); step >= slowInferenceAuditThreshold {
			t.Logf("advance: nonce %d step took %s (mock engine latency is 0; see stall audit above)", next, step.Round(time.Millisecond))
		}
	}
}
