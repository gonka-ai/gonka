//go:build testenvci

package citest

import (
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
)

// TestJaegerPromtailRegression keeps the legacy jaeger-promtail profile green
// while tempo-alloy is the e2e default.
func TestJaegerPromtailRegression(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)
	t.Setenv("TESTENV_OBS_PROFILE", string(harness.ObsProfileJaegerPromtail))

	stack, cfg, eps, obs := harness.BootObservabilityStack(t, "citest-jaeger-pt-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "jaeger", "promtail", "loki")
		}
	})

	requireProfile := obs.Profile == harness.ObsProfileJaegerPromtail
	if !requireProfile {
		t.Fatalf("expected profile %s, got %s", harness.ObsProfileJaegerPromtail, obs.Profile)
	}
	t.Logf("citest: legacy profile=%s otel=%s", obs.Profile, obs.Profile.OTELEndpoint())

	harness.WaitObservabilityReady(t, obs, 3*time.Minute)
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	model := config.PrimaryModelID(cfg)
	req := harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest jaeger-promtail regression"},
		},
		MaxTokens: 32,
	}
	harness.Step(t, "POST %s/v1/chat/completions (jaeger-promtail profile)", eps.GatewayHTTP)
	resp, _ := harness.PostGatewayChatCompletionEx(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req)
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)

	traceID := harness.WaitTraceCoveringServices(t, obs,
		[]string{"devshardctl", "devshardd"},
		2*time.Minute,
	)
	t.Logf("citest: jaeger shared trace_id=%s", traceID)
	harness.RequireLogsForTrace(t, obs, traceID, []string{"devshardctl", "versiond.*"}, 2*time.Minute)
	harness.WaitTraceSpan(t, obs, "devshardctl", "gateway.request", 30*time.Second)
	harness.WaitTraceSpan(t, obs, "devshardd", "devshardd.request", 30*time.Second)
}
