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
