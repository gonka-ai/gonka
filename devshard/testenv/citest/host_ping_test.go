//go:build testenvci

package citest

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

const (
	hostPingUpSeries         = "devshard_gateway_host_ping_up"
	hostPingTargetsSeries    = "devshard_gateway_host_ping_targets"
	hostPingTicksSeries      = "devshard_gateway_host_ping_ticks_total"
	hostPingRTTSeries        = "devshard_gateway_host_ping_rtt_seconds"
	hostPingWarmRTTSeries    = "devshard_gateway_host_ping_warm_rtt_seconds"
	hostPingLastProbeSeries  = "devshard_gateway_host_ping_last_probe_timestamp_seconds"
	hostClockDivergence      = "devshard_gateway_host_clock_divergence_seconds"
	hostEjectedSeries        = "devshard_gateway_host_ejected"
	participantKeyLabel      = "participant_key"
	divergenceSourceLabel    = "source"
	clockDivergenceSource    = "clock"
	dateDivergenceSource     = "date"
	hostPingWait             = 45 * time.Second
	hostPingUp               = 1.0
	hostPingDown             = 0.0
	hostPingTargetsCleared   = 0.0
	hostPingTargetsDropWait  = 15 * time.Second
	hostPingTargetsDropCheck = 200 * time.Millisecond
)

// TestHostPing exercises the gateway's host ping (E2–E6 + F3): the hosts of the live escrow are probed and labelled by participant key.
// Surface B (dapi_mlnode_ping_*) is out of testenv — covered by dapi unit tests.
func TestHostPing(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootHostPingStack(t, "citest-host-ping-*")
	client := harness.GatewayChatClient()
	metricsURL := harness.GatewayMetricsURL(eps)
	version := cfg.Versiond.VersionName
	if version == "" {
		version = "v2"
	}
	model := config.PrimaryModelID(cfg)
	adminKey := harness.TestenvAdminAPIKey

	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "gateway", "versiond-0", "versiond-1", "versiond-router")
		}
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

	escrowID := harness.GetGatewayEscrowID(t, client, eps.GatewayHTTP)
	harness.Step(t, "E2: wait for a ping of escrow %s's hosts", escrowID)
	pinged := waitPingedParticipant(t, client, metricsURL, escrowParticipants(t, client, eps.GatewayHTTP, escrowID))
	participant := map[string]string{participantKeyLabel: pinged}
	// Cold-dial samples are excluded from warm RTT — wait until a reused probe lands.
	harness.WaitMetricGauge(t, client, metricsURL, hostPingRTTSeries, participant, func(value float64) bool { return value > 0 }, hostPingWait)

	var firstFreshness float64
	t.Run("E2_metrics_after_chat", func(t *testing.T) {
		metrics := harness.ParseMetricsText(harness.FetchMetricsText(t, client, metricsURL))

		up, ok := harness.FindMetric(metrics, hostPingUpSeries, participant)
		require.True(t, ok)
		require.Equal(t, hostPingUp, up.Value)

		require.GreaterOrEqual(t, harness.HistogramSampleCount(metrics, hostPingWarmRTTSeries), 1.0)

		fresh, ok := harness.FindMetric(metrics, hostPingLastProbeSeries, participant)
		require.True(t, ok)
		require.Greater(t, fresh.Value, 0.0)
		firstFreshness = fresh.Value

		targets, ok := harness.FindMetric(metrics, hostPingTargetsSeries, nil)
		require.True(t, ok)
		require.Greater(t, targets.Value, hostPingTargetsCleared)

		ticks, ok := harness.FindMetric(metrics, hostPingTicksSeries, nil)
		require.True(t, ok)
		require.Greater(t, ticks.Value, 0.0)
	})

	t.Run("E3_date_or_ping_divergence_present", func(t *testing.T) {
		metrics := harness.ParseMetricsText(harness.FetchMetricsText(t, client, metricsURL))
		_, dateOK := harness.FindMetric(metrics, hostClockDivergence,
			map[string]string{participantKeyLabel: pinged, divergenceSourceLabel: dateDivergenceSource})
		_, pingOK := harness.FindMetric(metrics, hostClockDivergence,
			map[string]string{participantKeyLabel: pinged, divergenceSourceLabel: clockDivergenceSource})
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
		harness.WaitMetricGauge(t, client, metricsURL, hostClockDivergence,
			map[string]string{participantKeyLabel: pinged, divergenceSourceLabel: clockDivergenceSource},
			func(float64) bool { return true }, hostPingWait)

		// Freshness should advance across ticks (no flap back to date required here).
		harness.WaitMetricGauge(t, client, metricsURL, hostPingLastProbeSeries, participant,
			func(value float64) bool { return value > firstFreshness }, hostPingWait)
	})

	t.Run("E4_deactivate_clears_series", func(t *testing.T) {
		harness.PostAdminDeactivateDevshard(t, client, eps.GatewayHTTP, adminKey, escrowID)

		harness.WaitMetricAbsent(t, client, metricsURL, hostPingUpSeries, participant, hostPingWait)
		require.Eventually(t, func() bool {
			targetsMetric, ok := harness.FindMetric(harness.ParseMetricsText(harness.FetchMetricsText(t, client, metricsURL)), hostPingTargetsSeries, nil)
			return !ok || targetsMetric.Value == hostPingTargetsCleared
		}, hostPingTargetsDropWait, hostPingTargetsDropCheck, "host_ping_targets must drop to 0 after deactivate")
		harness.RequireNoMetric(t, harness.ParseMetricsText(harness.FetchMetricsText(t, client, metricsURL)), hostPingLastProbeSeries, participant)
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

	t.Run("F3_host_down_no_ejection", func(t *testing.T) {
		newEscrowID := harness.GetGatewayEscrowID(t, client, eps.GatewayHTTP)
		pinged := waitPingedParticipant(t, client, metricsURL, escrowParticipants(t, client, eps.GatewayHTTP, newEscrowID))
		participant := map[string]string{participantKeyLabel: pinged}

		before := ejectedHosts(t, client, metricsURL)
		// Fail only {prefix}/clock. Stopping versiond would also fail real traffic, which is not a ping leak.
		harness.FailChildClock(t, stack, cfg)

		deadline := time.Now().Add(15 * time.Second)
		var clockStatus int
		for time.Now().Before(deadline) {
			clockStatus, _, _ = harness.FetchChildPingHeaders(t, client, eps.RouterHTTP, version)
			if clockStatus == http.StatusServiceUnavailable {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		require.Equal(t, http.StatusServiceUnavailable, clockStatus,
			"clock fault file must 503 /%s/clock (rebuild linux binary: make -C testenv build-devshardd)", version)

		harness.WaitMetricGauge(t, client, metricsURL, hostPingUpSeries, participant,
			func(value float64) bool { return value == hostPingDown }, hostPingWait)
		require.Equal(t, before, ejectedHosts(t, client, metricsURL), "a probe outage must not eject a host")

		okReq := harness.ChatCompletionRequest{
			Model: model,
			Messages: []harness.ChatMessage{
				{Role: "user", Content: "citest host-ping clock fault chat still works"},
			},
			MaxTokens: 16,
		}
		okResp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, adminKey, okReq)
		harness.RequireMockOpenAIContent(t, okResp.Choices[0].Message.Content)
		require.Equal(t, before, ejectedHosts(t, client, metricsURL), "chat after clock fault must not eject a host")
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
	harness.PatchComposeEnvKey(t, stack.ComposePath, "DEVSHARD_GATEWAY_HOST_PING_DISABLED", `"true"`)
	cfg := stack.LoadConfig(t)
	require.Len(t, cfg.Hosts, 2)
	stack.Up(t)
	eps := stack.Endpoints(t, cfg)
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "gateway", "versiond-0", "versiond-1")
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
	_, pinged := harness.FindMetric(metrics, hostPingUpSeries, nil)
	require.False(t, pinged, "kill switch must leave no host ping series")
	if ticksMetric, ok := harness.FindMetric(metrics, hostPingTicksSeries, nil); ok {
		require.Equal(t, 0.0, ticksMetric.Value, "kill switch must not start the scheduler")
	}
}

func ejectedHosts(t *testing.T, client *http.Client, metricsURL string) float64 {
	t.Helper()
	var ejected float64
	for _, metric := range harness.ParseMetricsText(harness.FetchMetricsText(t, client, metricsURL)) {
		if metric.Name == hostEjectedSeries {
			ejected += metric.Value
		}
	}
	return ejected
}

func escrowParticipants(t *testing.T, client *http.Client, gatewayURL, escrowID string) []string {
	t.Helper()
	var body struct {
		Participants []string `json:"participants"`
	}
	require.NoError(t, harness.GetGatewayAdminJSON(client, gatewayURL+"/v1/admin/devshards/"+escrowID+"/participants", &body))
	require.NotEmpty(t, body.Participants, "escrow %s lists no participants", escrowID)
	return body.Participants
}

// waitPingedParticipant returns the participant the gateway labels a successful ping of the escrow's hosts with: one address is pinged once, under one of the participants it serves.
func waitPingedParticipant(t *testing.T, client *http.Client, metricsURL string, participants []string) string {
	t.Helper()
	pinged := ""
	require.Eventually(t, func() bool {
		for _, metric := range harness.ParseMetricsText(harness.FetchMetricsText(t, client, metricsURL)) {
			if metric.Name == hostPingUpSeries && metric.Value == hostPingUp && slices.Contains(participants, metric.Labels[participantKeyLabel]) {
				pinged = metric.Labels[participantKeyLabel]
				return true
			}
		}
		return false
	}, hostPingWait, time.Second, "no participant of %v was pinged up", participants)
	return pinged
}
