//go:build testenvci

package citest

import (
	"net/http"
	"testing"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"
)

// TestMLUpstream5xx verifies gateway chat fails when mock-openai returns HTTP 503.
func TestMLUpstream5xx(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootAdversarialStack(t, "citest-ml-upstream-5xx-*")
	client := harness.GatewayChatClient()
	mockOpenAI := eps.MockOpenAIHTTP
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, mockOpenAI)
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "mock-openai-0", "versiond-0", "versiond-1")
		}
	})

	status := http.StatusServiceUnavailable
	harness.PatchMockOpenAIFault(t, client, mockOpenAI, mockopenai.FaultPatch{HTTPStatus: &status})
	harness.PatchAdversarialFastTimeouts(t, client, eps.MockDapiHTTP)

	req := harness.ChatCompletionRequest{
		Model: config.PrimaryModelID(cfg),
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest ml-upstream-5xx unique prompt"},
		},
		MaxTokens: 16,
	}
	harness.Step(t, "non-stream chat with mock-openai http_status=503 should fail at gateway")
	code := harness.PostGatewayChatExpectFailure(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req)
	if code == 0 {
		harness.Step(t, "observed gateway transport timeout (ML/refusal path)")
	} else {
		harness.Step(t, "observed gateway HTTP %d", code)
	}
}
