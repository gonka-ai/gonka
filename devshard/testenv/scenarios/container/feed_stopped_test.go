//go:build testenvci

package container

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

const metricOracleFailures = "devshard_heightsync_oracle_failures_total"

// TestContainerE2E_HeightSync_FeedStoppedOmits covers CONTAINER_E2E_PLAN §5.7 / §7.3 Phase C:
// sync-turn lead Anchors while height-sync is up; after compose stop + MOCKDAPI_STALE_AFTER the
// next nonce in the initial sync window Omits on user request emit and host inbound — matching
// in-process §9.3 item 8 (peer-attestation response is not logged when SSE omits height_sync).
func TestContainerE2E_HeightSync_FeedStoppedOmits(t *testing.T) {
	ws, project, httpClient, streamClient, _ := startHeightSyncContainerStack(t)
	ctx := context.Background()
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}

	lead := nextSyncTurnLeadNonce(fetchDevshardctlNextNonce(t, httpClient))
	omitNonce := lead + 1
	if !heightSyncInSyncTurn(omitNonce) {
		t.Fatalf("sanity: omit nonce %d must stay in sync turn after lead %d", omitNonce, lead)
	}
	t.Logf("feed stopped: lead anchor nonce %d omit nonce %d", lead, omitNonce)

	advanceSessionToNonce(t, ctx, streamClient, lead)
	if err := postChatCompletionStream(t, ctx, streamClient, int(lead)); err != nil {
		t.Fatalf("lead nonce %d: %v", lead, err)
	}

	t.Cleanup(func() {
		dctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = DockerCompose(dctx, ws, project, nil, nil, "start", "height-sync").Run()
	})

	if err := DockerCompose(ctx, ws, project, os.Stdout, os.Stderr, "stop", "height-sync").Run(); err != nil {
		t.Fatalf("compose stop height-sync: %v", err)
	}
	t.Log("stopped height-sync; waiting for mockdapi StaleAfter")
	waitMockdapiStaleQuietPeriod(t)

	baselineOracle := PromInstantScalar(t, httpClient, fmt.Sprintf("sum(%s)", metricOracleFailures))

	advanceSessionToNonce(t, ctx, streamClient, omitNonce)
	if err := postChatCompletionStream(t, ctx, streamClient, int(omitNonce)); err != nil {
		t.Fatalf("omit nonce %d: %v", omitNonce, err)
	}

	lokiStart := LokiWindowStart()
	logQLCtlEmit := `{service_name="devshardctl"} |= "heightsync: emit"`
	logQLHost := `{service_name="devshardd-testenv"} |= "heightsync: peer attestation received"`
	deadline := time.Now().Add(3 * time.Minute)
	var sawReq, sawHost bool
	for time.Now().Before(deadline) {
		end := time.Now().Add(30 * time.Second)
		if !sawReq {
			sawReq = waitLokiHeightSyncEmitMode(t, httpClient, logQLCtlEmit, lokiStart, end, int(omitNonce), "request", "omit", 2*time.Second)
		}
		if !sawHost {
			sawHost = waitLokiHostPeerAttestationMode(t, httpClient, logQLHost, lokiStart, end, int(omitNonce), "omit", 2*time.Second)
		}
		if sawReq && sawHost {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !sawReq {
		t.Fatalf(`Loki: missing devshardctl request heightsync: emit mode=omit for nonce %d`, omitNonce)
	}
	if !sawHost {
		t.Fatalf(`Loki: missing host peer attestation mode=omit for nonce %d`, omitNonce)
	}

	promDeadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(promDeadline) {
		after := PromInstantScalar(t, httpClient, fmt.Sprintf("sum(%s)", metricOracleFailures))
		if after-baselineOracle >= 0.5 {
			t.Logf("oracle_failures_total Δ=%g (baseline=%g)", after-baselineOracle, baselineOracle)
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("Prometheus: expected increase in %s after omit at nonce %d", metricOracleFailures, omitNonce)
}

// TestContainerE2E_HeightSync_FeedRecovers covers CONTAINER_E2E_PLAN §5.7 / §7.3 Phase C:
// anchor at sync-turn lead while feed is up, omit through end of initial window with feed down,
// then anchor again at the next periodic sync-turn lead after height-sync restarts.
func TestContainerE2E_HeightSync_FeedRecovers(t *testing.T) {
	ws, project, httpClient, streamClient, _ := startHeightSyncContainerStack(t)
	ctx := context.Background()
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}

	// Prior subtest may leave height-sync stopped; bring feed up before anchoring.
	if err := DockerCompose(ctx, ws, project, nil, nil, "start", "height-sync").Run(); err != nil {
		t.Fatalf("compose start height-sync: %v", err)
	}
	waitHeightSyncStackReady(t, ctx, httpClient)

	lead := nextSyncTurnLeadNonce(fetchDevshardctlNextNonce(t, httpClient))
	recoverNonce := nextSyncTurnLeadAfter(lead + heightSyncSyncSlots - 1)
	if recoverNonce <= lead {
		t.Fatalf("sanity: recover nonce %d must follow lead %d", recoverNonce, lead)
	}
	t.Logf("feed recovers: lead=%d drain through %d then anchor at %d", lead, recoverNonce-1, recoverNonce)

	advanceSessionToNonce(t, ctx, streamClient, lead)
	if err := postChatCompletionStream(t, ctx, streamClient, int(lead)); err != nil {
		t.Fatalf("lead nonce %d: %v", lead, err)
	}

	if err := DockerCompose(ctx, ws, project, os.Stdout, os.Stderr, "stop", "height-sync").Run(); err != nil {
		t.Fatalf("compose stop height-sync: %v", err)
	}
	waitMockdapiStaleQuietPeriod(t)

	advanceSessionToNonce(t, ctx, streamClient, recoverNonce)

	if err := DockerCompose(ctx, ws, project, os.Stdout, os.Stderr, "start", "height-sync").Run(); err != nil {
		t.Fatalf("compose start height-sync: %v", err)
	}
	waitHeightSyncStackReady(t, ctx, httpClient)
	// Do not restart devshardctl/hosts here: mockdapi SSE reconnects on its own after
	// height-sync returns; an extra compose restart races the recover POST and triggers
	// TIMEOUT_REASON_REFUSED (RefusalTimeout) before executors are ready.
	waitHeightSyncFeedFreshAfterRestart(t, httpClient)
	// height-sync /block/latest can advance before devshardctl's long-lived SSE client
	// receives a new header; restart devshardctl only (keep hosts) to refresh oracle cache.
	restartDevshardctlForOracleReconnect(t, ws, project, ctx, httpClient)

	emitSince := time.Now().Add(-10 * time.Second)
	if err := postChatCompletionStream(t, ctx, streamClient, int(recoverNonce)); err != nil {
		t.Fatalf("recover nonce %d: %v", recoverNonce, err)
	}
	assertHeightSyncRequestEmit(t, ws, project, httpClient, int(recoverNonce), "anchor", emitSince)
}
