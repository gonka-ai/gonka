//go:build testenvci

package citest

import (
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
)

// TestTraceLogCorrelation asserts that a gateway chat produces one shared
// trace_id across gateway + host spans and Loki log lines from both
// compose_service families (devshardctl and versiond).
func TestTraceLogCorrelation(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps, obs := harness.BootObservabilityStack(t, "citest-trace-corr-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "jaeger", "promtail", "loki")
		}
	})

	harness.WaitObservabilityReady(t, obs, 3*time.Minute)
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	model := config.PrimaryModelID(cfg)
	req := harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest trace/log correlation"},
		},
		MaxTokens: 32,
	}
	harness.Step(t, "POST %s/v1/chat/completions (trace/log correlation probe)", eps.GatewayHTTP)
	resp, hdr := harness.PostGatewayChatCompletionEx(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req)
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)
	requestID := hdr.Get("X-Request-Id")
	t.Logf("citest: gateway X-Request-Id=%q", requestID)

	t.Logf("citest: observability profile=%s otel=%s", obs.Profile, obs.Profile.OTELEndpoint())

	// One trace spanning gateway (devshardctl) and host (devshardd).
	traceID := harness.WaitTraceCoveringServices(t, obs,
		[]string{"devshardctl", "devshardd"},
		2*time.Minute,
	)
	t.Logf("citest: shared trace_id=%s", traceID)

	// Loki lines for that trace_id from both compose_service families.
	harness.RequireLogsForTrace(t, obs, traceID, []string{
		"devshardctl",
		"versiond.*",
	}, 2*time.Minute)

	// Sanity: gateway.request + host request spans exist (operations).
	harness.WaitTraceSpan(t, obs, "devshardctl", "gateway.request", 30*time.Second)
	harness.WaitTraceSpan(t, obs, "devshardd", "devshardd.request", 30*time.Second)
}
