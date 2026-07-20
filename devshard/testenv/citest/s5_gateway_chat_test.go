//go:build testenvci

package citest

import (
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// TestS5_GatewayChat verifies devshardctl pooled /v1/chat/completions reaches mock-openai
// through versiond-router and devshardd for both non-stream and SSE stream responses.
func TestS5_GatewayChat(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootS1Stack(t, "citest-s5-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "mock-openai")
		}
	})
	harness.WaitS1Healthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz", 5*time.Minute, "devshardd health via router", stack)

	gatewayURL := eps.GatewayHTTP
	model := config.PrimaryModelID(cfg)
	adminKey := harness.TestenvAdminAPIKey

	nonStreamReq := harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest s5 non-stream"},
		},
		MaxTokens: 32,
	}
	harness.Step(t, "POST %s/v1/chat/completions (stream=false)", gatewayURL)
	resp := harness.PostGatewayChatCompletion(t, client, gatewayURL, adminKey, nonStreamReq)
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)
	require.Equal(t, "assistant", resp.Choices[0].Message.Role)

	streamReq := harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest s5 stream gateway"},
		},
		MaxTokens: 32,
	}
	harness.Step(t, "POST %s/v1/chat/completions (stream=true)", gatewayURL)
	streamContent, _ := harness.PostGatewayChatCompletionStream(t, client, gatewayURL, adminKey, streamReq)
	harness.RequireMockOpenAIContent(t, streamContent)
}
