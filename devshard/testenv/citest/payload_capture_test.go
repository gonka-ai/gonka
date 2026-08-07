//go:build testenvci

package citest

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

func TestC5a_PayloadCaptureHTTP503(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps, obs := harness.BootPayloadCaptureStack(t, "citest-c5a-503-*", "full")
	client := harness.GatewayChatClient()
	mockOpenAI := eps.MockOpenAIHTTP
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, mockOpenAI)
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "mock-openai-0", "versiond-0", "alloy", "loki")
		}
	})

	harness.WaitObservabilityReady(t, obs, 3*time.Minute)
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	status := http.StatusServiceUnavailable
	harness.PatchMockOpenAIFault(t, client, mockOpenAI, mockopenai.FaultPatch{HTTPStatus: &status})
	harness.PatchAdversarialFastTimeouts(t, client, eps.MockDapiHTTP)
	harness.PatchAdversarialFastRedundancy(t, client, eps.GatewayHTTP)
	// Threshold 1 so a single empty_stream strike produces a quarantine transition
	// (and the size-only payload_quarantine line) on the F1 path.
	harness.PatchGatewayAdminSettings(t, client, eps.GatewayHTTP, map[string]any{
		"participant_throttle": map[string]any{
			"empty_stream_threshold": 1,
		},
	})

	needle := fmt.Sprintf("citest-c5a-f1-%d", time.Now().UnixNano())
	req := harness.ChatCompletionRequest{
		Model: config.PrimaryModelID(cfg),
		Messages: []harness.ChatMessage{
			{Role: "user", Content: needle},
		},
		MaxTokens: 16,
	}
	harness.Step(t, "drive ML 503 failure for payload capture")
	_ = harness.PostGatewayChatExpectFailure(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req)

	line := harness.WaitPayloadCapturedLog(t, obs, needle, 3*time.Minute)
	require.NotEmpty(t, line["devshard.prompt.sha256"])
	require.NotEmpty(t, line["failed_at"])
	require.NotNil(t, line["response_ms"])
	// Body may be empty after host receipt; status/headers fallback is OK for F1.
	if rb, _ := line["response_bytes"].(float64); rb == 0 {
		require.True(t, line["http_status"] != nil || line["response_headers"] != nil,
			"empty body must include status/headers fallback: %v", line)
	}

	q := harness.WaitPayloadQuarantineLog(t, obs, 3*time.Minute)
	require.Equal(t, "payload_quarantine", q["stage"])
	require.NotNil(t, q["request_bytes"], "quarantine should log sizes")
	_, hasPromptHash := q["devshard.prompt.sha256"]
	_, hasResponseBody := q["response"]
	require.False(t, hasPromptHash, "quarantine must not log prompt fingerprints/bodies")
	require.False(t, hasResponseBody, "quarantine must not log response bodies")
	// logging.Stage always sets request=<request_id>; that is not a body field.
	if req, ok := q["request"].(string); ok {
		require.False(t, len(req) > 0 && req[0] == '{', "quarantine must not log request JSON body: %q", req)
	}
}

func TestC5a_PayloadCapturePartialStream(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps, obs := harness.BootPayloadCaptureStack(t, "citest-c5a-partial-*", "full")
	client := harness.GatewayChatClient()
	mockOpenAI := eps.MockOpenAIHTTP
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, mockOpenAI)
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "mock-openai-0", "versiond-0", "alloy", "loki")
		}
	})

	harness.WaitObservabilityReady(t, obs, 3*time.Minute)
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	partial := true
	harness.PatchMockOpenAIFault(t, client, mockOpenAI, mockopenai.FaultPatch{PartialStream: &partial})
	harness.PatchAdversarialFastTimeouts(t, client, eps.MockDapiHTTP)
	harness.PatchAdversarialFastRedundancy(t, client, eps.GatewayHTTP)

	needle := fmt.Sprintf("citest-c5a-partial-%d", time.Now().UnixNano())
	req := harness.ChatCompletionRequest{
		Model: config.PrimaryModelID(cfg),
		Messages: []harness.ChatMessage{
			{Role: "user", Content: needle + " " + "partial stream body please make this long enough"},
		},
		MaxTokens: 64,
		Stream:    true,
	}
	harness.Step(t, "stream chat with partial_stream (no EOS)")
	_, _, _ = harness.PostGatewayChatStreamResult(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req)

	line := harness.WaitPayloadCapturedLog(t, obs, needle, 3*time.Minute)
	require.NotEmpty(t, line["devshard.prompt.sha256"])
	require.NotNil(t, line["response_ms"])
	require.NotEmpty(t, line["failed_at"])
	rb, _ := line["response_bytes"].(float64)
	require.Greater(t, rb, float64(0), "partial stream must log a non-empty response body sample: %v", line)
	respField, hasResp := line["response"].(string)
	require.True(t, hasResp && respField != "", "full level must include response body text for partial stream")
}

func TestC5a_PayloadCaptureSSEError(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps, obs := harness.BootPayloadCaptureStack(t, "citest-c5a-sse-*", "full")
	client := harness.GatewayChatClient()
	mockOpenAI := eps.MockOpenAIHTTP
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, mockOpenAI)
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "mock-openai-0", "versiond-0", "alloy", "loki")
		}
	})

	harness.WaitObservabilityReady(t, obs, 3*time.Minute)
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	msg := "TimeoutError"
	harness.PatchMockOpenAIFault(t, client, mockOpenAI, mockopenai.FaultPatch{SSEErrorMessage: &msg})
	harness.PatchAdversarialFastTimeouts(t, client, eps.MockDapiHTTP)

	needle := fmt.Sprintf("citest-c5a-sse-%d", time.Now().UnixNano())
	req := harness.ChatCompletionRequest{
		Model: config.PrimaryModelID(cfg),
		Messages: []harness.ChatMessage{
			{Role: "user", Content: needle},
		},
		MaxTokens: 32,
		Stream:    true,
	}
	harness.Step(t, "stream chat with vLLM-shaped SSE error")
	_, _, _ = harness.PostGatewayChatStreamResult(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req)

	line := harness.WaitPayloadCapturedLog(t, obs, needle, 3*time.Minute)
	require.NotEmpty(t, line["devshard.prompt.sha256"])
	require.NotNil(t, line["response_ms"])
	// Prefer body sample or structured error fields.
	rb, _ := line["response_bytes"].(float64)
	errType, _ := line["error_type"].(string)
	errMsg, _ := line["error_message"].(string)
	require.True(t, rb > 0 || errType != "" || errMsg != "",
		"sse error must leave response sample or error_* fields: %v", line)
}
