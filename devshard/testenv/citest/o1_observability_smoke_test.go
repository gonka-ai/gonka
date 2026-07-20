//go:build testenvci

package citest

import (
	"fmt"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
)

// TestO1_ObservabilitySmoke starts the S1 stack with Jaeger/Loki/Prometheus/Grafana,
// runs a gateway chat, and asserts devshardd spans and structured logs appear.
func TestO1_ObservabilitySmoke(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps, obs := harness.BootS1ObsStack(t, "citest-o1-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "jaeger", "promtail")
		}
	})

	harness.WaitObservabilityReady(t, obs, 3*time.Minute)
	harness.WaitS1Healthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	model := config.PrimaryModelID(cfg)
	adminKey := harness.TestenvAdminAPIKey
	req := harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest o1 observability smoke"},
		},
		MaxTokens: 32,
	}
	harness.Step(t, "POST %s/v1/chat/completions (observability probe)", eps.GatewayHTTP)
	resp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, adminKey, req)
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)

	version := cfg.Versiond.VersionName
	if version == "" {
		version = "v2"
	}

	harness.WaitJaegerSpan(t, obs, "devshardd", "devshardd.request", 2*time.Minute)
	harness.WaitJaegerSpan(t, obs, "devshardd", "devshardd.inference", 2*time.Minute)
	harness.WaitLokiSubstring(t, obs, "devshard request terminal", 2*time.Minute)

	harness.RequireMetricsBody(t, client, fmt.Sprintf("%s/%s/metrics", eps.RouterHTTP, version), "devshardd_request_duration_seconds")
	harness.RequireMetricsBody(t, client, eps.GatewayHTTP+"/metrics", "devshard_http_requests_total")
}
