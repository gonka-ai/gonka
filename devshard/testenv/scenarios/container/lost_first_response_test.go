//go:build testenvci

package container

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestContainerE2E_HeightSync_LostFirstResponse covers CONTAINER_E2E_PLAN §5.3 / §7.2 Phase B.
//
// Session nonce advances on PrepareInference (StartInference in the diff); Confirm may arrive
// while ML still runs. Stopping a host before POST cannot reproduce a lost first response —
// we arm DEVSHARDD_DEBUG hold so the host processes the diff but sends no SSE/receipt, then
// stop the executor while the proxy is blocked (mirrors in-process Prepare + kill before SendOnly).
//
// Flow: advance to sync-turn lead (1, 8, 16, …), arm hold on executor, POST lead, stop host,
// expect SendOnly failed; restart host, POST lead+1, expect request Anchor in Loki.
func TestContainerE2E_HeightSync_LostFirstResponse(t *testing.T) {
	ws, project, httpClient, streamClient, _ := startHeightSyncContainerStack(t)
	ctx := context.Background()
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}

	next := fetchDevshardctlNextNonce(t, httpClient)
	lead, recoverNonce := nextLostFirstResponseScenario(next)
	killService := hostServiceForNonce(lead)
	t.Logf("lost-first-response: next=%d lead=%d (logical 1) recover=%d (logical 2) kill=%s",
		next, lead, recoverNonce, killService)

	advanceSessionToNonce(t, ctx, streamClient, lead)

	t.Cleanup(func() {
		dctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = DockerCompose(dctx, ws, project, nil, nil, "start", killService).Run()
		dl := time.Now().Add(3 * time.Minute)
		WaitCoreStackServicesRunningOrFail(t, dctx, ws, project, dl)
	})

	armHostInferenceHold(t, int(lead%4))

	ctxFail, cancelFail := context.WithCancel(ctx)
	defer cancelFail()
	go func() {
		if err := postChatCompletionStream(t, ctxFail, streamClient, int(lead)); err != nil {
			t.Logf("lead nonce %d stream ended: %v", lead, err)
		}
	}()
	time.Sleep(2 * time.Second)

	stop := DockerCompose(ctx, ws, project, os.Stdout, os.Stderr, "stop", killService)
	if err := stop.Run(); err != nil {
		t.Fatalf("docker compose stop %s: %v", killService, err)
	}
	t.Logf("stopped %s while lead %d blocked on hold (no SSE)", killService, lead)

	waitComposeServiceLog(t, ws, project, "devshardctl", 30*time.Second,
		fmt.Sprintf("devshardctl inference nonce=%d", lead), "SendOnly failed")
	cancelFail()

	start := DockerCompose(ctx, ws, project, os.Stdout, os.Stderr, "start", killService)
	if err := start.Run(); err != nil {
		t.Fatalf("docker compose start %s: %v", killService, err)
	}
	WaitCoreStackServicesRunningOrFail(t, ctx, ws, project, time.Now().Add(4*time.Minute))

	if err := postChatCompletionStream(t, ctx, streamClient, int(recoverNonce)); err != nil {
		t.Fatalf("nonce %d: %v", recoverNonce, err)
	}

	lokiStart := time.Now().Add(-2 * time.Minute)
	logQLCtl := `{service_name="devshardctl"} |= "heightsync: emit"`
	deadline := time.Now().Add(3 * time.Minute)
	var sawRecoverAnchor bool
	for time.Now().Before(deadline) {
		end := time.Now().Add(30 * time.Second)
		for _, ln := range LokiQueryRange(t, httpClient, logQLCtl, lokiStart, end, 5000) {
			kv := ParseLogKV(ln)
			if kv["msg"] != "heightsync: emit" || kv["direction"] != "request" {
				continue
			}
			if !strings.EqualFold(kv["mode"], "anchor") {
				continue
			}
			if parseNonce(kv["nonce"]) == int(recoverNonce) {
				sawRecoverAnchor = true
				break
			}
		}
		if sawRecoverAnchor {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !sawRecoverAnchor {
		t.Fatalf(`Loki: expected devshardctl "heightsync: emit" request mode=anchor for nonce %d`, recoverNonce)
	}
}
