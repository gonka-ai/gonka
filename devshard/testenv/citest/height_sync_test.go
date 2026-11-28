//go:build testenvci

package citest

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"devshard/heightsync"
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
			harness.DumpComposeLogs(t, stack, "gateway", "versiond-0", "versiond-1", "mock-dapi")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	postHeightSyncChat(t, cfg, eps, "citest height-sync cadence")
	logs := stack.WaitComposeLogsContain(t, 2*time.Minute, "heightsync: emit",
		"gateway", "versiond-0", "versiond-1")
	require.True(t, harness.LogsContainFields(logs, harness.LogStringField("mode", "anchor")),
		"first inference is a sync-turn / session-start Anchor")
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
			harness.DumpComposeLogs(t, stack, "gateway", "mock-openai", "versiond-0", "versiond-1")
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
			harness.DumpComposeLogs(t, stack, "gateway", "versiond-0", "versiond-1", "mock-dapi")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	postHeightSyncChat(t, cfg, eps, "citest height-sync before feed stop")
	stack.WaitComposeLogsContain(t, 2*time.Minute, "heightsync: emit",
		"gateway", "versiond-0", "versiond-1")

	harness.Step(t, "pause mock-dapi (oracle feed stop)")
	stack.PauseService(t, "mock-dapi")
	paused = true
	time.Sleep(24 * time.Second)

	postHeightSyncChat(t, cfg, eps, "citest height-sync while feed stopped")
	var stopped string
	ok := harness.AssertEventually(t, 2*time.Minute, 2*time.Second, func() bool {
		logTail, err := stack.ComposeLogsTail(400, "gateway", "versiond-0", "versiond-1")
		if err != nil {
			return false
		}
		stopped = logTail
		return harness.LogsContainFields(logTail, harness.LogStringField("mode", "omit")) || strings.Contains(logTail, "tip_stale_after_ms")
	})
	require.True(t, ok, "paused oracle should Omit or emit a degraded Anchor; logs:\n%s", stopped)

	harness.Step(t, "unpause mock-dapi (oracle recover)")
	stack.UnpauseService(t, "mock-dapi")
	paused = false
	harness.WaitGETOK(t, harness.HTTPClient(), eps.MockDapiHTTP+"/healthz", 2*time.Minute, "mock-dapi healthz after unpause", stack)
	harness.WaitGETOK(t, harness.HTTPClient(), eps.MockDapiHTTP+"/block/1", 2*time.Minute, "mock-dapi /block/1 after unpause", stack)
	time.Sleep(3 * time.Second)

	postHeightSyncChat(t, cfg, eps, "citest height-sync after feed recover")
	var recovered string
	ok = harness.AssertEventually(t, 2*time.Minute, 2*time.Second, func() bool {
		logTail, err := stack.ComposeLogsTail(400, "gateway", "versiond-0", "versiond-1")
		if err != nil {
			return false
		}
		recovered = logTail
		return harness.LogsContainFields(logTail, harness.LogStringField("mode", "anchor"))
	})
	require.True(t, ok, "compose logs missing mode=anchor within 2m\n%s", recovered)
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
			harness.DumpComposeLogs(t, stack, "gateway", "versiond-0", "versiond-1", "mock-dapi")
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
		"gateway", "versiond-0", "versiond-1")
	require.NotContains(t, logs, "light_block", "hash-only / old dapi must not claim Strong")
}

func TestContainerE2E_HeightSync_QuietEscrowHeartbeat(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootHeightSyncStack(t, "citest-hs-h26-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "gateway", "versiond-0", "versiond-1")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	// §10.3.1: F is host-signed and lands on the next nonce. Seed only warms
	// the envelope; a session that never infers never heartbeats.
	seedHeightSyncFloor(t, cfg, eps)

	// skipped_no_height is expected until F exists; after the bootstrap the
	// first quiet turn is due one Interval later (6s).
	escrowID := harness.GetGatewayEscrowID(t, nil, eps.GatewayHTTP)
	waitHeartbeatOpened(t, eps, escrowID)
	logs, err := stack.ComposeLogsTail(800, "gateway")
	require.NoError(t, err)
	repair := strings.Count(logs, "repair request") + strings.Count(logs, "RepairProbe")
	require.Zero(t, repair, "healthy quiet path must send zero repair probes")

	harness.WaitHeightSyncView(t, client, eps.GatewayHTTP, escrowID, heightSyncWait, func(view harness.HeightSyncView) bool {
		return len(view.PeerSeen) > 0
	})

	// Anchor seal / empty-height counters move once the tip has passed D_ack
	// (H44/H45). Either is enough — tip motion is not under our control.
	harness.WaitHeightSyncView(t, client, eps.GatewayHTTP, escrowID, heightSyncWait, func(view harness.HeightSyncView) bool {
		return view.AnchorsLastSealed != nil || view.BlocksWithoutAnchor > 0
	})
}

func TestContainerE2E_HeightSync_OneHostStopped(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	// H43 is one unreachable slot. The default 2-container stack is an HA pair
	// of one identity: versiond-0 owns every slot and versiond-1 owns none, so
	// stopping the replica still lets Q complete. HA pair + solo (3 containers,
	// 2 slots) makes stopping the solo drop a real roster slot.
	stack, cfg, eps := harness.BootHeightSyncHAPlusSoloStack(t, "citest-hs-h27-*")
	solo := harness.FirstSoloHostID(t, cfg)
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "gateway", "versiond-0", "versiond-1", solo)
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	seedHeightSyncFloor(t, cfg, eps)

	escrowID := harness.GetGatewayEscrowID(t, nil, eps.GatewayHTTP)
	waitHeartbeatOpened(t, eps, escrowID)

	harness.Step(t, "stop "+solo+" (solo identity / one slot down)")
	stack.StopService(t, solo)

	var logs string
	ok := harness.AssertEventually(t, 2*time.Minute, 2*time.Second, func() bool {
		logTail, err := stack.ComposeLogsTail(800, "gateway", "versiond-0")
		if err != nil {
			return false
		}
		logs = logTail
		return strings.Contains(logTail, "turn_abandoned") || strings.Contains(logTail, "heartbeat span dispatched")
	})
	require.True(t, ok, "cadence must keep running after one host stops; logs:\n%s", logs)
	require.LessOrEqual(t, strings.Count(logs, "repair request"), 8, "repair probes stay bounded")
	require.NotContains(t, logs, "close_ready_armed=1",
		"a live host still receiving heartbeats must not arm just because a peer is down")

	// One unreachable slot must show up as a planned cadence disposition, not
	// silent reopen. Compose mock-chain usually seals D_ack before TurnTimeout,
	// so the producer SettleTurn path emits turn_settled_degraded (plan §8.12.3).
	// abandoned turns are the TurnTimeout path; unit H43 covers that in
	// isolation. Either count moving means the stuck turn was accounted for.
	harness.WaitHeightSyncView(t, client, eps.GatewayHTTP, escrowID, heightSyncWait, func(view harness.HeightSyncView) bool {
		return view.AbandonedTurns >= 1 || view.CadenceCount(heightsync.CadenceTurnSettledDegraded) >= 1
	})
}

// TestContainerE2E_HeightSync_BusyEscrowDischarge is H42 on compose. The
// producer sleeps until lastTurnover+Interval, so stamped traffic keeps that
// deadline ahead of the clock and heartbeat_opened stays put. A quiet interval
// after the stamps is when the next heartbeat opens.
func TestContainerE2E_HeightSync_BusyEscrowDischarge(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	// Q stamp turnover needs two distinct SlotIDs. The default 2-container
	// stack is one identity: versiond-0 owns both slots and heartbeat acks
	// already take Q. HA pair + solo is two executors, matching unit H2.
	stack, cfg, eps := harness.BootHeightSyncHAPlusSoloStack(t, "citest-hs-h42-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "gateway", "versiond-0", "versiond-1",
				harness.FirstSoloHostID(t, cfg))
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	escrowID := harness.GetGatewayEscrowID(t, nil, eps.GatewayHTTP)
	interval := heightsync.DefaultHeartbeatInterval
	// One pair stamps both slots (nonce%2) and arms the deadline at
	// lastTurnover+Interval. Sample after that, then keep both slots stamped
	// for a full interval. Boot may already have opened quiet heartbeats, so
	// the assertion is that this counter does not move.
	postHeightSyncChat(t, cfg, eps, "citest height-sync busy discharge seed slot-a")
	postHeightSyncChat(t, cfg, eps, "citest height-sync busy discharge seed slot-b")
	opened := heartbeatOpenedCount(t, eps, escrowID)

	busyUntil := time.Now().Add(interval)
	for time.Now().Before(busyUntil) {
		postHeightSyncChat(t, cfg, eps, "citest height-sync busy discharge slot-a")
		postHeightSyncChat(t, cfg, eps, "citest height-sync busy discharge slot-b")
	}
	require.Equal(t, opened, heartbeatOpenedCount(t, eps, escrowID),
		"stamped traffic must keep the heartbeat deadline ahead of the clock")

	harness.WaitHeightSyncView(t, client, eps.GatewayHTTP, escrowID, interval+busyDischargeSlack, func(view harness.HeightSyncView) bool {
		return view.CadenceCount(heightsync.CadenceHeartbeatOpened) > opened
	})
}

// TestContainerE2E_HeightSync_StaleClaimSpread is H40: after a host stops
// acking past freshness F, its claim turns stale while the height spread keeps
// counting it, so the alertable number does not silently shrink.
func TestContainerE2E_HeightSync_StaleClaimSpread(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootHeightSyncStack(t, "citest-hs-h40-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "gateway", "versiond-0", "versiond-1")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	postHeightSyncChat(t, cfg, eps, "citest height-sync seed tip")

	escrowID := harness.GetGatewayEscrowID(t, nil, eps.GatewayHTTP)
	before := harness.WaitHeightSyncView(t, client, eps.GatewayHTTP, escrowID, heightSyncWait, func(view harness.HeightSyncView) bool {
		_, claimed := view.HeightSpread()
		return claimed && view.FreshTips() >= 1
	})
	spreadBefore, _ := before.HeightSpread()

	harness.Step(t, "stop versiond-1 so its tip goes stale past F")
	stack.StopService(t, "versiond-1")

	// DefaultOriginatorFreshness is 120s. Keep the live host talking so the
	// gateway tip and the remaining claim stay fresh.
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		postHeightSyncChat(t, cfg, eps, "citest height-sync keep live tip fresh")
		time.Sleep(5 * time.Second)
	}

	after := harness.WaitHeightSyncView(t, client, eps.GatewayHTTP, escrowID, heightSyncWait, func(view harness.HeightSyncView) bool {
		spread, claimed := view.HeightSpread()
		return view.StaleTips() >= 1 && view.FreshTips() >= 1 && claimed && spread >= spreadBefore
	})
	spreadAfter, _ := after.HeightSpread()
	require.GreaterOrEqual(t, spreadAfter, spreadBefore,
		"spread must not silently shrink when a host goes quiet")
}

// seedHeightSyncFloor is citest's §10.3.1 bootstrap. Chat 1's start is
// hashless; confirm/finish ride chat 2 and raise F. After this the escrow
// still owes Interval of quiet before heartbeat_opened: executor stamps
// set lastTurnoverFromStamp, so extra chat during that wait substitutes
// discharged_by_inference instead.
func seedHeightSyncFloor(t *testing.T, cfg *config.File, eps harness.Endpoints) {
	t.Helper()
	postHeightSyncChat(t, cfg, eps, "citest height-sync seed floor a")
	postHeightSyncChat(t, cfg, eps, "citest height-sync seed floor b")
}

func heartbeatOpenedCount(t *testing.T, eps harness.Endpoints, escrowID string) uint64 {
	t.Helper()
	view, listed := harness.GetHeightSyncView(t, nil, eps.GatewayHTTP, harness.TestenvAdminAPIKey, escrowID)
	require.True(t, listed, "escrow %s missing from the height-sync view", escrowID)
	return view.CadenceCount(heightsync.CadenceHeartbeatOpened)
}

func waitHeartbeatOpened(t *testing.T, eps harness.Endpoints, escrowID string) harness.HeightSyncView {
	t.Helper()
	return harness.WaitHeightSyncView(t, nil, eps.GatewayHTTP, escrowID, heightSyncWait, func(view harness.HeightSyncView) bool {
		return view.CadenceCount(heightsync.CadenceHeartbeatOpened) >= 1
	})
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
