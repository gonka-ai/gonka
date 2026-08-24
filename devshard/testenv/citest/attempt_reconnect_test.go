//go:build testenvci

package citest

import (
	"strings"
	"testing"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

// TestAttemptReconnect_AdminEnables verifies reconnect knobs are accepted on
// POST /v1/admin/settings (in-process ApplyRedundancySettings).
func TestAttemptReconnect_AdminEnables(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, _, eps := harness.BootAdversarialStack(t, "citest-attempt-reconnect-admin-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1")
		}
	})

	harness.Step(t, "enable attempt reconnect via admin settings")
	harness.PatchGatewayAttemptReconnect(t, client, eps.GatewayHTTP, true, 1000, 2)
}

// TestAttemptReconnect_V2ProtocolSkipsSameNonce: with reconnect enabled on a ≤v4
// (citest default v2) session, a mid-stream ML fault must not produce a silent
// same-nonce resume.
func TestAttemptReconnect_V2ProtocolSkipsSameNonce(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootAdversarialStack(t, "citest-attempt-reconnect-v2-*")
	client := harness.GatewayChatClient()
	mockOpenAI := eps.MockOpenAIHTTP
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, mockOpenAI)
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "mock-openai")
		}
	})

	// ≤v4 path: do not require /v1/status session_version (optional field).
	// Stack VersionName is the bind we stamp; reconnect is gated in-process.
	require.True(t, strings.Contains(cfg.Versiond.VersionName, "2") || cfg.Versiond.VersionName == "v2",
		"expected v2-bound stack for protocol-gate negative path, got %q", cfg.Versiond.VersionName)

	harness.PatchGatewayAttemptReconnect(t, client, eps.GatewayHTTP, true, 1000, 2)

	partial := true
	harness.PatchMockOpenAIFault(t, client, mockOpenAI, mockopenai.FaultPatch{PartialStream: &partial})

	req := harness.ChatCompletionRequest{
		Model: config.PrimaryModelID(cfg),
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest attempt-reconnect v2 no same-nonce resume unique prompt"},
		},
		MaxTokens: 48,
	}
	harness.Step(t, "stream chat with PartialStream; v2 must not same-nonce-resume to success")
	_, err := harness.TryPostGatewayChatCompletionStream(client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req)
	require.Error(t, err, "v2 + PartialStream must not complete as a clean resumed stream")
}

// TestAttemptReconnect_V5MidStreamDetachResumesSameNonce: v5 + reconnect enabled
// + primary detach mid-stream → one continuous completion and one finish.
func TestAttemptReconnect_V5MidStreamDetachResumesSameNonce(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootReconnectStackWithPrimaryDetach(t, "citest-attempt-reconnect-v5-*", "v5", 1)
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, eps.MockOpenAIHTTP)
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "mock-openai")
		}
	})

	ver := harness.RequireGatewaySessionVersion(t, client, eps.GatewayHTTP)
	require.True(t, strings.Contains(ver, "5"), "expected v5-bound session, got %q", ver)

	harness.PatchGatewayAttemptReconnect(t, client, eps.GatewayHTTP, true, 2000, 2)
	// Slow ML chunks so the primary detach (after 1 write) lands mid-stream,
	// not after a single batched write of the full completion.
	delayMS := 40
	harness.PatchMockOpenAIFault(t, client, eps.MockOpenAIHTTP, mockopenai.FaultPatch{
		StreamChunkDelay: &delayMS,
	})

	req := harness.ChatCompletionRequest{
		Model: config.PrimaryModelID(cfg),
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest attempt-reconnect v5 same-nonce resume unique prompt"},
		},
		MaxTokens: 48,
	}
	harness.Step(t, "stream chat through mid-stream detach + same-nonce resume")
	content, sawDone := harness.PostGatewayChatCompletionStream(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req)
	require.NotEmpty(t, content)
	require.True(t, sawDone, "resumed stream should reach [DONE]")
	harness.RequireMockOpenAIContent(t, content)
	harness.RequireGatewayReconnectResumed(t, stack)
	harness.RequireExactlyOneFinishedInference(t, client, eps.GatewayHTTP)
}
