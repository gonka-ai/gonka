//go:build testenvci

package citest

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

// TestHeightSync_MockDapiBlockAt is the 0.2.15-v5 stand-in: mock-dapi
// mounts GET /block/:height. Real dapi cannot replace mock-dapi in this
// stack because mock-chain is not CometBFT.
func TestHeightSync_MockDapiBlockAt(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, _, eps := harness.BootStack(t, "citest-hs-latest-*")
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "mock-dapi")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)

	client := harness.HTTPClient()
	h1 := mockDapiMaxHeight(t, client, eps.MockDapiHTTP)
	require.Greater(t, h1, int64(0), "GET /block/:height")
	deadline := time.Now().Add(15 * time.Second)
	var h2 int64
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		h2 = mockDapiMaxHeight(t, client, eps.MockDapiHTTP)
		if h2 > h1 {
			break
		}
	}
	require.Greater(t, h2, h1, "mock-dapi /block/:height should advance")
}

// TestHeightSync_CadenceEmitsAnchor is height-sync against new dapi (0.2.15-v5
// /block/*). First inference is a session-start Anchor.
func TestHeightSync_CadenceEmitsAnchor(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootHeightSyncStack(t, "citest-hs-cadence-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "mock-dapi")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	postHeightSyncChat(t, cfg, eps, "citest height-sync cadence")
	logs := stack.WaitComposeLogsContain(t, 2*time.Minute, "heightsync: emit",
		"devshardctl", "versiond-0", "versiond-1")
	require.Contains(t, logs, "mode=anchor", "first inference is a sync-turn / session-start Anchor")
}

func TestHeightSync_LostFirstChunk(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootHeightSyncStack(t, "citest-hs-lost-*")
	client := harness.GatewayChatClient()
	mockOpenAI := eps.MockOpenAIHTTP
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, mockOpenAI)
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "mock-openai", "versiond-0", "versiond-1")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	drop := true
	harness.PatchMockOpenAIFault(t, client, mockOpenAI, mockopenai.FaultPatch{DropFirstChunk: &drop})

	harness.Step(t, "stream chat with height-sync on and drop_first_chunk=true")
	content, _ := harness.PostGatewayChatCompletionStream(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
		Model: config.PrimaryModelID(cfg),
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest height-sync lost first chunk unique prompt"},
		},
		MaxTokens: 32,
	})
	require.NotEmpty(t, content)
	require.True(t, strings.HasPrefix(content, "ock-openai:"),
		"drop_first_chunk should remove the leading rune from mock-openai echo, got %q", content)
}

func TestHeightSync_FeedStoppedOmitsThenRecovers(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootHeightSyncStack(t, "citest-hs-feed-*")
	client := harness.GatewayChatClient()
	paused := false
	t.Cleanup(func() {
		if paused {
			stack.UnpauseService(t, "mock-dapi")
		}
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "mock-dapi")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	postHeightSyncChat(t, cfg, eps, "citest height-sync before feed stop")
	stack.WaitComposeLogsContain(t, 2*time.Minute, "heightsync: emit",
		"devshardctl", "versiond-0", "versiond-1")

	harness.Step(t, "pause mock-dapi (oracle feed stop)")
	stack.PauseService(t, "mock-dapi")
	paused = true
	time.Sleep(12 * time.Second)

	postHeightSyncChat(t, cfg, eps, "citest height-sync while feed stopped")
	var stopped string
	ok := harness.AssertEventually(t, 2*time.Minute, 2*time.Second, func() bool {
		out, err := stack.ComposeLogsTail(400, "devshardctl", "versiond-0", "versiond-1")
		if err != nil {
			return false
		}
		stopped = out
		return strings.Contains(out, "mode=omit") || strings.Contains(out, "tip_stale_after_ms")
	})
	require.True(t, ok, "paused oracle should Omit or emit a degraded Anchor; logs:\n%s", stopped)

	harness.Step(t, "unpause mock-dapi (oracle recover)")
	stack.UnpauseService(t, "mock-dapi")
	paused = false
	harness.WaitGETOK(t, harness.HTTPClient(), eps.MockDapiHTTP+"/healthz", 2*time.Minute, "mock-dapi healthz after unpause", stack)
	harness.WaitGETOK(t, harness.HTTPClient(), eps.MockDapiHTTP+"/block/1", 2*time.Minute, "mock-dapi /block/1 after unpause", stack)
	time.Sleep(3 * time.Second)

	postHeightSyncChat(t, cfg, eps, "citest height-sync after feed recover")
	recovered := stack.WaitComposeLogsContain(t, 2*time.Minute, "mode=anchor",
		"devshardctl", "versiond-0", "versiond-1")
	require.Contains(t, recovered, "heightsync: emit")
}

// TestHeightSync_LegacyDapiChatCompletes is the 0.2.15 stand-in: mock-dapi
// omits /block/* the way a dapi built from ak/height-sync-protocol (no mount)
// does. Chat still completes; Strong is never claimed. Direct-chain failover
// cannot Anchor on mock-chain (no Comet /block), so emit is Omit — same as D7.
func TestHeightSync_LegacyDapiChatCompletes(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootHeightSyncLegacyDapiStack(t, "citest-hs-legacy-dapi-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "mock-dapi")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)

	resp, err := harness.HTTPClient().Get(eps.MockDapiHTTP + "/block/1")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "0.2.15 dapi has no /block/*")

	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	postHeightSyncChat(t, cfg, eps, "citest height-sync legacy dapi 0.2.15")
	logs := stack.WaitComposeLogsContain(t, 2*time.Minute, "heightsync: emit",
		"devshardctl", "versiond-0", "versiond-1")
	require.NotContains(t, logs, "light_block", "hash-only / old dapi must not claim Strong")
}

func TestContainerE2E_HeightSync_QuietEscrowHeartbeat(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, _, eps := harness.BootHeightSyncStack(t, "citest-hs-h26-*")
	client := harness.GatewayChatClient()
	admin := harness.TestenvAdminAPIKey
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	logs := stack.WaitComposeLogsContain(t, 2*time.Minute, "heightsync: cadence", "devshardctl")
	require.True(t, strings.Contains(logs, "heartbeat_opened") || strings.Contains(logs, "heartbeat span dispatched"),
		"quiet escrow must open heartbeat turns; logs:\n%s", logs)
	repair := strings.Count(logs, "repair request") + strings.Count(logs, "RepairProbe")
	require.Zero(t, repair, "healthy quiet path must send zero repair probes")

	metricsURL := eps.GatewayHTTP + "/metrics"
	body := harness.WaitMetricsPredicate(t, client, metricsURL, 2*time.Minute, func(b string) bool {
		v, ok := harness.MetricLineValue(b, "devshard_gateway_heightsync_cadence_events_total",
			map[string]string{"event": "heartbeat_opened"})
		return ok && v >= 1
	})
	require.NotContains(t, body, "devshard_gateway_heightsync_peer_seen{",
		"peer matrix series stay off by default (H48)")
	body = harness.WaitMetricsPredicate(t, client, metricsURL, 2*time.Minute, func(b string) bool {
		return strings.Contains(b, "devshard_gateway_heightsync_peer_seen_count")
	})
	require.Contains(t, body, "devshard_gateway_heightsync_peer_seen_count",
		"linear peer_seen_count remains on by default")
	require.NotContains(t, body, "devshard_gateway_heightsync_peer_seen{",
		"peer matrix series stay off by default (H48)")

	// Anchor seal / empty-height counters become visible once the tip has
	// moved past D_ack (H44/H45). Either a sealed last-block gauge or the
	// without-anchor counter is enough — tip motion is not under our control.
	harness.WaitMetricsPredicate(t, client, metricsURL, 2*time.Minute, func(b string) bool {
		return strings.Contains(b, "devshard_gateway_heightsync_anchors_last_block") ||
			strings.Contains(b, "devshard_gateway_heightsync_blocks_without_anchor_total")
	})

	var debug struct {
		Escrows []map[string]any `json:"escrows"`
	}
	harness.GetDebugHeightSync(t, client, eps.GatewayHTTP, admin, &debug)
	require.NotEmpty(t, debug.Escrows, "GET /v1/debug/heightsync must list the live escrow")
	require.Contains(t, debug.Escrows[0], "cadence_events")
	require.Contains(t, debug.Escrows[0], "peer_seen")
}

func TestContainerE2E_HeightSync_OneHostStopped(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, _, eps := harness.BootHeightSyncStack(t, "citest-hs-h27-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	metricsURL := eps.GatewayHTTP + "/metrics"
	harness.WaitMetricsPredicate(t, client, metricsURL, 2*time.Minute, func(b string) bool {
		v, ok := harness.MetricLineValue(b, "devshard_gateway_heightsync_cadence_events_total",
			map[string]string{"event": "heartbeat_opened"})
		return ok && v >= 1
	})

	harness.Step(t, "stop versiond-1 (one host down)")
	stack.StopService(t, "versiond-1")

	var logs string
	ok := harness.AssertEventually(t, 2*time.Minute, 2*time.Second, func() bool {
		out, err := stack.ComposeLogsTail(800, "devshardctl", "versiond-0")
		if err != nil {
			return false
		}
		logs = out
		return strings.Contains(out, "turn_abandoned") || strings.Contains(out, "heartbeat span dispatched")
	})
	require.True(t, ok, "cadence must keep running after one host stops; logs:\n%s", logs)
	require.LessOrEqual(t, strings.Count(logs, "repair request"), 8, "repair probes stay bounded")
	require.NotContains(t, logs, "close_ready_armed=1",
		"a live host still receiving heartbeats must not arm just because a peer is down")

	// One unreachable slot must show up as a planned cadence disposition, not
	// silent reopen. Compose mock-chain usually seals D_ack before TurnTimeout,
	// so the producer SettleTurn path emits turn_settled_degraded (plan §8.12.3).
	// turns_abandoned_total is the TurnTimeout path; unit H43 covers that in
	// isolation. Either counter moving means the stuck turn was accounted for.
	harness.WaitMetricsPredicate(t, client, metricsURL, 2*time.Minute, func(b string) bool {
		abandoned, aok := harness.MetricLineValue(b, "devshard_gateway_heightsync_turns_abandoned_total", nil)
		settled, sok := harness.MetricLineValue(b, "devshard_gateway_heightsync_cadence_events_total",
			map[string]string{"event": "turn_settled_degraded"})
		return (aok && abandoned >= 1) || (sok && settled >= 1)
	})
}

// TestContainerE2E_HeightSync_BusyEscrowDischarge is H42 on compose: stamped
// inference traffic must show up as discharged_by_inference rather than an
// absence of heartbeats.
func TestContainerE2E_HeightSync_BusyEscrowDischarge(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootHeightSyncStack(t, "citest-hs-h42-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	metricsURL := eps.GatewayHTTP + "/metrics"
	harness.WaitMetricsPredicate(t, client, metricsURL, 2*time.Minute, func(b string) bool {
		v, ok := harness.MetricLineValue(b, "devshard_gateway_heightsync_cadence_events_total",
			map[string]string{"event": "heartbeat_opened"})
		return ok && v >= 1
	})

	// MaybeRecordDischarged needs a stamp turnover and no open heartbeat turn.
	// Sleeping Interval between chats leaves a quiet window the heartbeat loop
	// wins; burst inside Interval so executor stamps can form Q first.
	for i := 0; i < 8; i++ {
		postHeightSyncChat(t, cfg, eps, "citest height-sync busy discharge")
	}

	body := harness.WaitMetricsPredicate(t, client, metricsURL, 2*time.Minute, func(b string) bool {
		v, ok := harness.MetricLineValue(b, "devshard_gateway_heightsync_cadence_events_total",
			map[string]string{"event": "discharged_by_inference"})
		return ok && v >= 1
	})
	discharged, _ := harness.MetricLineValue(body, "devshard_gateway_heightsync_cadence_events_total",
		map[string]string{"event": "discharged_by_inference"})
	require.GreaterOrEqual(t, discharged, 1.0)

	var debug struct {
		Escrows []struct {
			CadenceEvents []map[string]any `json:"cadence_events"`
		} `json:"escrows"`
	}
	harness.GetDebugHeightSync(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, &debug)
	require.NotEmpty(t, debug.Escrows)
	found := false
	for _, ev := range debug.Escrows[0].CadenceEvents {
		if ev["event"] == "discharged_by_inference" {
			found = true
			break
		}
	}
	require.True(t, found, "debug ring must record the substitution explicitly")
}

// TestContainerE2E_HeightSync_StaleClaimSpread is H40: after a host stops
// acking past freshness F, its host_height disappears and claim age rises,
// while height_spread keeps the stale claim so the alertable number does not
// silently shrink.
func TestContainerE2E_HeightSync_StaleClaimSpread(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootHeightSyncStack(t, "citest-hs-h40-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	postHeightSyncChat(t, cfg, eps, "citest height-sync seed tip")

	metricsURL := eps.GatewayHTTP + "/metrics"
	before := harness.WaitMetricsPredicate(t, client, metricsURL, 2*time.Minute, func(b string) bool {
		_, ok := harness.MetricLineValue(b, "devshard_gateway_heightsync_height_spread", nil)
		return ok && strings.Contains(b, "devshard_gateway_heightsync_host_height{")
	})
	spreadBefore, _ := harness.MetricLineValue(before, "devshard_gateway_heightsync_height_spread", nil)

	harness.Step(t, "stop versiond-1 so its tip goes stale past F")
	stack.StopService(t, "versiond-1")

	// DefaultOriginatorFreshness is 60s. Keep the live host talking so the
	// gateway tip and the remaining claim stay fresh.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		postHeightSyncChat(t, cfg, eps, "citest height-sync keep live tip fresh")
		time.Sleep(5 * time.Second)
	}

	body := harness.WaitMetricsPredicate(t, client, metricsURL, 2*time.Minute, func(b string) bool {
		ages := 0
		for _, line := range strings.Split(b, "\n") {
			if strings.HasPrefix(line, "devshard_gateway_heightsync_host_claim_age_seconds{") {
				ages++
			}
		}
		heights := strings.Count(b, "devshard_gateway_heightsync_host_height{")
		spread, hasSpread := harness.MetricLineValue(b, "devshard_gateway_heightsync_height_spread", nil)
		return ages >= 1 && heights >= 1 && hasSpread && spread >= spreadBefore
	})
	require.Contains(t, body, "devshard_gateway_heightsync_host_claim_age_seconds",
		"stale slot must raise claim age")
	spreadAfter, _ := harness.MetricLineValue(body, "devshard_gateway_heightsync_height_spread", nil)
	require.GreaterOrEqual(t, spreadAfter, spreadBefore,
		"spread must not silently shrink when a host goes quiet")
}

// TestContainerE2E_HeightSync_SettleDropsSeries is H47: after the escrow is
// retired, no gateway height-sync series may still carry its label. Admin
// deactivate uses the same retireRuntime registry drop as settle/rotation.
func TestContainerE2E_HeightSync_SettleDropsSeries(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootHeightSyncStack(t, "citest-hs-h47-*")
	client := harness.HTTPClient()
	chatClient := harness.GatewayChatClient()
	admin := harness.TestenvAdminAPIKey
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "mock-chain", "mock-dapi", "versiond-0", "versiond-1")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, chatClient, eps.GatewayHTTP, 3*time.Minute, stack)
	postHeightSyncChat(t, cfg, eps, "citest height-sync before settle")

	escrowID := harness.GetGatewayEscrowID(t, client, eps.GatewayHTTP)
	metricsURL := eps.GatewayHTTP + "/metrics"
	harness.WaitMetricsPredicate(t, client, metricsURL, 2*time.Minute, func(b string) bool {
		return harness.AnyHeightSyncMetricHasLabel(b, "devshard_id", escrowID)
	})

	harness.Step(t, "retire escrow %s via admin deactivate (same registry drop as settle)", escrowID)
	harness.PostAdminDeactivateDevshard(t, client, eps.GatewayHTTP, admin, escrowID)
	harness.WaitGatewayEscrowRetired(t, client, eps.GatewayHTTP, escrowID, 2*time.Minute)

	harness.WaitMetricsPredicate(t, client, metricsURL, 2*time.Minute, func(b string) bool {
		return !harness.AnyHeightSyncMetricHasLabel(b, "devshard_id", escrowID)
	})
}

// TestContainerE2E_HeightSync_PeerMatrixOptIn is H48 with the env flag on:
// quadratic peer_seen series appear, while the debug surface always has the
// matrix regardless.
func TestContainerE2E_HeightSync_PeerMatrixOptIn(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootHeightSyncPeerMatrixStack(t, "citest-hs-h48-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	postHeightSyncChat(t, cfg, eps, "citest height-sync peer matrix")

	metricsURL := eps.GatewayHTTP + "/metrics"
	body := harness.WaitMetricsPredicate(t, client, metricsURL, 2*time.Minute, func(b string) bool {
		return strings.Contains(b, "devshard_gateway_heightsync_peer_seen{") &&
			strings.Contains(b, "devshard_gateway_heightsync_peer_seen_count{")
	})
	require.Contains(t, body, "observer_slot=")
	require.Contains(t, body, "subject_slot=")

	var debug struct {
		Escrows []map[string]any `json:"escrows"`
	}
	harness.GetDebugHeightSync(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, &debug)
	require.NotEmpty(t, debug.Escrows)
	require.Contains(t, debug.Escrows[0], "peer_seen")
}

func postHeightSyncChat(t *testing.T, cfg *config.File, eps harness.Endpoints, prompt string) {
	t.Helper()
	client := harness.GatewayChatClient()
	harness.Step(t, "POST %s/v1/chat/completions (%s)", eps.GatewayHTTP, prompt)
	resp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, harness.ChatCompletionRequest{
		Model: config.PrimaryModelID(cfg),
		Messages: []harness.ChatMessage{
			{Role: "user", Content: prompt},
		},
		MaxTokens: 32,
	})
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)
}

func mockDapiMaxHeight(t *testing.T, client *http.Client, mockDapiHTTP string) int64 {
	t.Helper()
	var max int64
	for h := int64(1); h < 10_000; h++ {
		resp, err := client.Get(fmt.Sprintf("%s/block/%d", mockDapiHTTP, h))
		if err != nil {
			break
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			break
		}
		max = h
	}
	return max
}
