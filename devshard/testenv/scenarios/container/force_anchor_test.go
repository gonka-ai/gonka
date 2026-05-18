//go:build testenvci

package container

import (
	"context"
	"testing"
	"time"
)

// TestContainerE2E_HeightSync_ForceAnchorSingleMessage covers CONTAINER_E2E_PLAN §5.4 / §7.2 Phase B:
// mirrors in-process TestHeightSyncAnchor_E2E_ForceAnchorOutsideSyncTurn — with K=8 and slots=4,
// force_height_sync_anchor composes MsgForceHeightSyncTurn and Anchors on the forced window
// (trigger nonce 7 mod 8, window trigger..trigger+3), not legacy single-message force at nonce 5.
func TestContainerE2E_HeightSync_ForceAnchorSingleMessage(t *testing.T) {
	ws, project, httpClient, streamClient, _ := startHeightSyncContainerStack(t)
	ctx := context.Background()
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}

	trigger := nextForceAnchorTrigger(fetchDevshardctlNextNonce(t, httpClient))
	if wantRequestAnchorAtNonce(int(trigger)) {
		t.Fatalf("sanity: nonce %d must be Omit under cadence without force", trigger)
	}
	t.Logf("force anchor: trigger nonce %d (window %d..%d)", trigger, trigger, trigger+3)

	advanceSessionToNonce(t, ctx, streamClient, trigger)

	emitSince := time.Now().Add(-10 * time.Second)
	forceBody := []byte(`{"model":"llama","stream":true,"max_tokens":50,"force_height_sync_anchor":true}`)
	if err := postChatCompletionStreamWithBody(t, ctx, streamClient, int(trigger), forceBody); err != nil {
		t.Fatalf("nonce %d (forced sync turn trigger): %v", trigger, err)
	}
	assertHeightSyncRequestEmit(t, ws, project, httpClient, int(trigger), "anchor", emitSince)
}
