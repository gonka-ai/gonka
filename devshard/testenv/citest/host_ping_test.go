//go:build testenvci

package citest

import (
	"net/http"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// TestHostPing exercises Surface A host-ping e2e (E1–E6 + F3).
// Surface B (dapi_mlnode_ping_*) is out of testenv — covered by dapi unit tests.
func TestHostPing(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootHostPingStack(t, "citest-host-ping-*")
	client := harness.GatewayChatClient()
	metricsURL := harness.GatewayMetricsURL(eps)
	dial := harness.ExpectedHostPingDial
	version := cfg.Versiond.VersionName
	if version == "" {
		version = "v2"
	}
	model := config.PrimaryModelID(cfg)
	adminKey := harness.TestenvAdminAPIKey

	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "versiond-router")
		}
	})

	t.Run("E1_unused_escrow_absent", func(t *testing.T) {
		body := harness.FetchMetricsText(t, client, metricsURL)
		metrics := harness.ParseMetricsText(body)
		if m, ok := harness.FindMetric(metrics, "devshard_gateway_host_ping_targets", nil); ok {
			require.Equal(t, 0.0, m.Value, "unused escrow must not enter ping target set")
		}
		require.False(t, harness.MetricHasLabelValue(metrics, "devshard_gateway_host_ping_up", "host", dial))
		require.False(t, harness.MetricHasLabelValue(metrics, "devshard_gateway_host_ping_participant_info", "host", dial))
	})

	harness.Step(t, "E2: one successful gateway chat")
	req := harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest host-ping e2"},
		},
		MaxTokens: 32,
	}
	resp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, adminKey, req)
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)

	harness.Step(t, "E2: wait for host-ping gauges (date or ping tier)")
	harness.WaitMetricGauge(t, client, metricsURL, "devshard_gateway_host_ping_up",
		map[string]string{"host": dial}, func(v float64) bool { return v == 1 }, 45*time.Second)
	// Cold-dial samples are excluded from warm RTT — wait until a reused probe lands.
	harness.WaitMetricGauge(t, client, metricsURL, "devshard_gateway_host_ping_rtt_seconds",
		map[string]string{"host": dial}, func(v float64) bool { return v > 0 }, 45*time.Second)

	var firstFreshness float64
	t.Run("E2_metrics_after_chat", func(t *testing.T) {
		body := harness.FetchMetricsText(t, client, metricsURL)
		metrics := harness.ParseMetricsText(body)

		up, ok := harness.FindMetric(metrics, "devshard_gateway_host_ping_up", map[string]string{"host": dial})
		require.True(t, ok)
		require.Equal(t, 1.0, up.Value)

		require.GreaterOrEqual(t, harness.HistogramSampleCount(metrics, "devshard_gateway_host_ping_warm_rtt_seconds"), 1.0)

		fresh, ok := harness.FindMetric(metrics, "devshard_gateway_host_ping_last_probe_timestamp_seconds", map[string]string{"host": dial})
		require.True(t, ok)
		require.Greater(t, fresh.Value, 0.0)
		firstFreshness = fresh.Value

		_, ok = harness.FindMetric(metrics, "devshard_gateway_host_ping_participant_info", map[string]string{"host": dial})
		require.True(t, ok, "participant_info mapping must be present")

		ticks, ok := harness.FindMetric(metrics, "devshard_gateway_host_ping_ticks_total", nil)
		require.True(t, ok)
		require.Greater(t, ticks.Value, 0.0)
	})

	t.Run("E3_date_or_ping_divergence_present", func(t *testing.T) {
		body := harness.FetchMetricsText(t, client, metricsURL)
		metrics := harness.ParseMetricsText(body)
		_, dateOK := harness.FindMetric(metrics, "devshard_gateway_host_clock_divergence_seconds",
			map[string]string{"host": dial, "source": "date"})
		_, pingOK := harness.FindMetric(metrics, "devshard_gateway_host_clock_divergence_seconds",
			map[string]string{"host": dial, "source": "clock"})
		require.True(t, dateOK || pingOK, "divergence series must exist (date or ping source)")
	})

	t.Run("E5_child_ping_via_versiond", func(t *testing.T) {
		status, recv, send := harness.FetchChildPingHeaders(t, client, eps.RouterHTTP, version)
		require.Equal(t, http.StatusNoContent, status)
		require.NotEmpty(t, recv)
		require.NotEmpty(t, send)

		// Bare router /clock must not falsely succeed as a child ping.
		resp, err := client.Get(eps.RouterHTTP + "/clock")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.NotEqual(t, http.StatusNoContent, resp.StatusCode,
			"bare /clock must not look like a successful child ping")
	})

	t.Run("E6_gateway_prefers_ping", func(t *testing.T) {
		harness.WaitMetricGauge(t, client, metricsURL, "devshard_gateway_host_ping_probe_kind",
			map[string]string{"host": dial, "kind": "clock"},
			func(v float64) bool { return v == 1 }, 45*time.Second)

		body := harness.FetchMetricsText(t, client, metricsURL)
		metrics := harness.ParseMetricsText(body)
		_, ok := harness.FindMetric(metrics, "devshard_gateway_host_clock_divergence_seconds",
			map[string]string{"host": dial, "source": "clock"})
		require.True(t, ok, "ping-source divergence expected after Step 3 child")

		// Freshness should advance across ticks (no flap back to date required here).
		harness.WaitMetricGauge(t, client, metricsURL, "devshard_gateway_host_ping_last_probe_timestamp_seconds",
			map[string]string{"host": dial},
			func(v float64) bool { return v > firstFreshness }, 45*time.Second)
	})

	t.Run("E4_deactivate_clears_series", func(t *testing.T) {
		escrowID := harness.GetGatewayEscrowID(t, client, eps.GatewayHTTP)
		harness.PostAdminDeactivateDevshard(t, client, eps.GatewayHTTP, adminKey, escrowID)

		harness.WaitMetricAbsent(t, client, metricsURL, "devshard_gateway_host_ping_up",
			map[string]string{"host": dial}, 45*time.Second)
		body := harness.FetchMetricsText(t, client, metricsURL)
		metrics := harness.ParseMetricsText(body)
		harness.RequireNoMetric(t, metrics, "devshard_gateway_host_ping_last_probe_timestamp_seconds",
			map[string]string{"host": dial})
		require.False(t, harness.MetricHasLabelValue(metrics, "devshard_gateway_host_ping_participant_info", "host", dial))
		if m, ok := harness.FindMetric(metrics, "devshard_gateway_host_ping_targets", nil); ok {
			require.Equal(t, 0.0, m.Value)
		}
	})

	t.Run("E4_new_escrow_chat_healthy", func(t *testing.T) {
		harness.PostAdminCreateEscrow(t, client, eps.GatewayHTTP, adminKey, model, 500_000)
		harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
		okReq := harness.ChatCompletionRequest{
			Model: model,
			Messages: []harness.ChatMessage{
				{Role: "user", Content: "citest host-ping after deactivate"},
			},
			MaxTokens: 16,
		}
		okResp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, adminKey, okReq)
		harness.RequireMockOpenAIContent(t, okResp.Choices[0].Message.Content)
	})

	t.Run("F3_host_down_no_quarantine", func(t *testing.T) {
		// Ensure dial is probed again after E4 recovery chat.
		harness.WaitMetricGauge(t, client, metricsURL, "devshard_gateway_host_ping_up",
			map[string]string{"host": dial}, func(v float64) bool { return v == 1 }, 45*time.Second)

		before := quarantineTransitionsSum(t, client, metricsURL)
		stack.StopService(t, "versiond-0")
		stack.StopService(t, "versiond-1")

		harness.WaitMetricGauge(t, client, metricsURL, "devshard_gateway_host_ping_up",
			map[string]string{"host": dial}, func(v float64) bool { return v == 0 }, 45*time.Second)

		after := quarantineTransitionsSum(t, client, metricsURL)
		require.Equal(t, before, after, "probe outage must not quarantine participants")
	})
}

// TestHostPingKillSwitch covers F4: disabled job does not probe; chat still works.
func TestHostPingKillSwitch(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack := harness.NewStack(t, "citest-host-ping-kill-*")
	harness.RequireLinuxDevshardd(t, stack.TestenvDir)
	harness.WriteStackConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	harness.PatchComposeInsertEnvAfter(t, stack.ComposePath, "GATEWAY_MAX_TOKENS_CAP",
		`DEVSHARD_GATEWAY_HOST_PING_DISABLED: "true"`,
	)
	cfg := stack.LoadConfig(t)
	require.Len(t, cfg.Hosts, 2)
	stack.Up(t)
	eps := stack.Endpoints(t, cfg)
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	model := config.PrimaryModelID(cfg)
	adminKey := harness.TestenvAdminAPIKey
	req := harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest host-ping kill switch"},
		},
		MaxTokens: 16,
	}
	resp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, adminKey, req)
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)

	// Give the (disabled) job time that would otherwise produce ticks.
	time.Sleep(4 * time.Second)
	body := harness.FetchMetricsText(t, client, harness.GatewayMetricsURL(eps))
	metrics := harness.ParseMetricsText(body)
	require.False(t, harness.MetricHasLabelValue(metrics, "devshard_gateway_host_ping_up", "host", harness.ExpectedHostPingDial))
	if m, ok := harness.FindMetric(metrics, "devshard_gateway_host_ping_ticks_total", nil); ok {
		require.Equal(t, 0.0, m.Value, "kill switch must not start the scheduler")
	}
}

func quarantineTransitionsSum(t *testing.T, client *http.Client, metricsURL string) float64 {
	t.Helper()
	body := harness.FetchMetricsText(t, client, metricsURL)
	var sum float64
	for _, m := range harness.ParseMetricsText(body) {
		if m.Name == "devshard_gateway_participant_quarantine_transitions_total" {
			sum += m.Value
		}
	}
	return sum
}
