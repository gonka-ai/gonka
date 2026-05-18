//go:build testenvci

package container

import (
	"context"
	"testing"
	"time"
)

// TestContainerE2E_HeightSync_Smoke covers CONTAINER_E2E_PLAN §5.8 / §7.3 Phase C:
// minimal compose gate — one inference at the next sync-turn lead must produce a heightsync
// request anchor (compose logs first, then Loki), with courier peer-tip warm-up when needed.
func TestContainerE2E_HeightSync_Smoke(t *testing.T) {
	ws, project, httpClient, streamClient, _ := startHeightSyncContainerStack(t)
	ctx := context.Background()
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}

	lead := nextSyncTurnLeadNonce(fetchDevshardctlNextNonce(t, httpClient))
	t.Logf("smoke: sync-turn lead nonce %d", lead)

	last := fetchDevshardctlLastNonce(t, httpClient)
	if lead > uint64(heightSyncSyncSlots) && last+1 < lead {
		warmNonce := lead - 1
		if heightSyncInSyncTurn(warmNonce) {
			warmCourierPeerTipsAtNonce(t, ctx, streamClient, ws, project, httpClient, warmNonce)
		}
	}

	advanceSessionToNonce(t, ctx, streamClient, lead)
	emitSince := time.Now().Add(-10 * time.Second)
	if err := postChatCompletionStream(t, ctx, streamClient, int(lead)); err != nil {
		t.Fatalf("nonce %d: %v", lead, err)
	}

	if isInitialCourierSyncTurn(int(lead)) {
		logQLCtl := `{service_name="devshardctl"} |= "heightsync: emit"`
		lokiEnd := time.Now().Add(30 * time.Second)
		if waitLokiHeightSyncEmitMode(t, httpClient, logQLCtl, emitSince, lokiEnd, int(lead), "request", "omit", 30*time.Second) {
			t.Logf("smoke: cold courier omit at initial sync-turn lead=%d (peer tips warm on host responses)", lead)
			return
		}
	}
	assertHeightSyncRequestEmit(t, ws, project, httpClient, int(lead), "anchor", emitSince)
}
