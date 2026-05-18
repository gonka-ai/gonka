//go:build testenvci

package container

import (
	"context"
	"testing"
	"time"
)

// TestContainerE2E_HeightSync_Smoke covers CONTAINER_E2E_PLAN §5.8 / §7.3 Phase C:
// minimal compose gate — one inference at the next sync-turn lead must produce at least one
// heightsync request Anchor in Loki (thin wrapper around the cadence path).
func TestContainerE2E_HeightSync_Smoke(t *testing.T) {
	_, _, httpClient, streamClient, _ := startHeightSyncContainerStack(t)
	ctx := context.Background()
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}

	lead := nextSyncTurnLeadNonce(fetchDevshardctlNextNonce(t, httpClient))
	t.Logf("smoke: sync-turn lead nonce %d", lead)

	advanceSessionToNonce(t, ctx, streamClient, lead)
	if err := postChatCompletionStream(t, ctx, streamClient, int(lead)); err != nil {
		t.Fatalf("nonce %d: %v", lead, err)
	}

	lokiStart := LokiWindowStart()
	logQLCtl := `{service_name="devshardctl"} |= "heightsync: emit"`
	if !waitLokiHeightSyncEmitMode(t, httpClient, logQLCtl, lokiStart, time.Now().Add(30*time.Second), int(lead), "request", "anchor", 3*time.Minute) {
		t.Fatalf(`smoke: no devshardctl request mode=anchor for nonce %d (stack or Loki unhealthy)`, lead)
	}
}
