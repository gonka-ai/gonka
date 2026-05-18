package container

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	heightSyncAnchorK    = 8
	heightSyncSyncSlots  = 4
	devshardctlStatusURL = "http://127.0.0.1:8081/v1/status"
	// mockdapiStaleAfter matches MOCKDAPI_STALE_AFTER in gencompose (container E2E).
	mockdapiStaleAfter = 3 * time.Second
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

// runSequentialInferenceWindow posts count consecutive chat-completions starting at startNonce.
// Each inference completes before the next starts so devshardctl and host escrow nonces stay aligned.
func runSequentialInferenceWindow(t *testing.T, ctx context.Context, c *http.Client, startNonce, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		n := startNonce + i
		if err := postChatCompletionStream(t, ctx, c, n); err != nil {
			t.Fatalf("inference %d: %w", n, err)
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
