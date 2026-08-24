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

// TestLostFirstChunk verifies streaming chat survives mock-openai dropping the first SSE chunk.
func TestLostFirstChunk(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootAdversarialStack(t, "citest-lost-first-chunk-*")
	client := harness.GatewayChatClient()
	mockOpenAI := eps.MockOpenAIHTTP
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, mockOpenAI)
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "mock-openai-0", "versiond-0", "versiond-1")
		}
	})

	drop := true
	harness.PatchMockOpenAIFault(t, client, mockOpenAI, mockopenai.FaultPatch{DropFirstChunk: &drop})

	req := harness.ChatCompletionRequest{
		Model: config.PrimaryModelID(cfg),
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest a1 lost first chunk unique prompt"},
		},
		MaxTokens: 32,
	}
	harness.Step(t, "stream chat with mock-openai drop_first_chunk=true")
	content, _ := harness.PostGatewayChatCompletionStream(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req)
	require.NotEmpty(t, content)
	require.True(t, strings.HasPrefix(content, "ock-openai:"),
		"drop_first_chunk should remove the leading rune from mock-openai echo, got %q", content)
}
