package main

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func TestGatewayCoreV1MetricsRecordBoundedLabels(t *testing.T) {
	m := NewDevshardMetrics()

	m.RecordParticipantLimitRejection("participant-1", "Qwen/Test", "transport_request")
	m.RecordParticipantTransportError("participant-1", "Qwen/Test", "inference", http.StatusServiceUnavailable)
	m.RecordGatewayRequest("Qwen/Test", "success", "none")
	m.RecordCriticalUserFailure("Qwen/Test", "runtime_unavailable")
	m.RecordGatewayHiddenFailure("Qwen/Test", "protected", "empty_stream")
	m.RecordGatewayUserVisibleWin("participant-1", "Qwen/Test")
	m.RecordGatewaySlotDecision(GatewaySlotDecisionMetric{
		ParticipantKey: "participant-1",
		Model:          "Qwen/Test",
		EscrowID:       "12",
		Decision:       "ghost_no_send",
		Reason:         "participant_throttled_no_send",
		QuarantineMode: "probe",
	})
	m.RecordGatewayAttemptStarted(GatewayAttemptStartMetric{
		ParticipantKey: "participant-1",
		Model:          "Qwen/Test",
		Role:           "extra",
		Reason:         "receipt_timeout",
		QuarantineMode: "none",
	})
	m.RecordGatewayAttemptTerminal(GatewayAttemptTerminalMetric{
		ParticipantKey: "participant-1",
		Model:          "Qwen/Test",
		Role:           "extra",
		Outcome:        "failed",
		Visibility:     "failed_not_finished",
	})
	m.RecordGatewayAttemptFailure(GatewayAttemptFailureMetric{
		ParticipantKey: "participant-1",
		Model:          "Qwen/Test",
		Role:           "extra",
		Reason:         "empty_stream",
		Visibility:     "failed_not_finished",
	})
	m.RecordGatewayNoWinnerAttempt("participant-1", "Qwen/Test", "shadow_quarantine", "shadow")
	m.RecordGatewayTimeoutAction(GatewayTimeoutActionMetric{
		ParticipantKey: "participant-1",
		Model:          "Qwen/Test",
		Kind:           "execution",
		Action:         "completed",
		Reason:         "none",
	})

	families, err := m.registry.Gather()
	require.NoError(t, err)
	requireMetricCounterValue(t, families, "devshard_gateway_participant_limit_rejections_total", map[string]string{"participant_key": "participant-1", "model": "Qwen/Test", "scope": "transport_request"}, 1)
	requireMetricCounterValue(t, families, "devshard_gateway_participant_transport_errors_total", map[string]string{"participant_key": "participant-1", "model": "Qwen/Test", "path_kind": "inference", "status": "503"}, 1)
	requireMetricCounterValue(t, families, "devshard_gateway_requests_total", map[string]string{"model": "Qwen/Test", "outcome": "success", "reason": "none"}, 1)
	requireMetricCounterValue(t, families, "devshard_gateway_critical_user_failures_total", map[string]string{"model": "Qwen/Test", "reason": "runtime_unavailable"}, 1)
	requireMetricCounterValue(t, families, "devshard_gateway_user_requests_with_hidden_failure_total", map[string]string{"model": "Qwen/Test", "severity": "protected", "reason": "empty_stream"}, 1)
	requireMetricCounterValue(t, families, "devshard_gateway_user_visible_wins_total", map[string]string{"participant_key": "participant-1", "model": "Qwen/Test"}, 1)
	requireMetricCounterValue(t, families, "devshard_gateway_slot_decisions_total", map[string]string{"participant_key": "participant-1", "model": "Qwen/Test", "escrow_id": "12", "decision": "ghost_no_send", "reason": "participant_throttled_no_send", "quarantine_mode": "probe"}, 1)
	requireMetricCounterValue(t, families, "devshard_gateway_attempts_started_total", map[string]string{"participant_key": "participant-1", "model": "Qwen/Test", "role": "extra", "reason": "receipt_timeout", "quarantine_mode": "none"}, 1)
	requireMetricCounterValue(t, families, "devshard_gateway_attempts_terminal_total", map[string]string{"participant_key": "participant-1", "model": "Qwen/Test", "role": "extra", "outcome": "failed", "visibility": "failed_not_finished"}, 1)
	requireMetricCounterValue(t, families, "devshard_gateway_attempt_failures_total", map[string]string{"participant_key": "participant-1", "model": "Qwen/Test", "role": "extra", "reason": "empty_stream", "visibility": "failed_not_finished"}, 1)
	requireMetricCounterValue(t, families, "devshard_gateway_no_winner_attempts_total", map[string]string{"participant_key": "participant-1", "model": "Qwen/Test", "reason": "shadow_quarantine", "quarantine_mode": "shadow"}, 1)
	requireMetricCounterValue(t, families, "devshard_gateway_timeout_actions_total", map[string]string{"participant_key": "participant-1", "model": "Qwen/Test", "kind": "execution", "action": "completed", "reason": "none"}, 1)
}

func TestGatewayMetricsCollectorIncludesParticipantQuarantineState(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)
	for i := 0; i < emptyStreamQuarantineThreshold; i++ {
		limiter.ObserveEmptyStreamForModel("participant-1", "Qwen/Test")
	}

	g := &Gateway{
		participantLimiter: limiter,
		runtimeOrder: []*devshardRuntime{{
			id:              "12",
			model:           "Qwen/Test",
			participantKeys: []string{"participant-1"},
		}},
	}
	collector := newGatewayMetricsCollectorWithHostConnections(g, fakeHostConnectionSnapshotter(nil))

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	families, err := registry.Gather()
	require.NoError(t, err)
	requireMetricGaugeValue(t, families, "devshard_gateway_participant_quarantine_state", map[string]string{"participant_key": "participant-1", "model": "Qwen/Test", "mode": "shadow"}, 1)
}

func TestGatewayMetricsCollectorDedupesParticipantQuarantineState(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)
	for i := 0; i < emptyStreamQuarantineThreshold; i++ {
		limiter.ObserveEmptyStreamForModel("participant-1", "Qwen/Test")
	}

	g := &Gateway{
		participantLimiter: limiter,
		runtimeOrder: []*devshardRuntime{
			{
				id:              "12",
				model:           "Qwen/Test",
				participantKeys: []string{"participant-1"},
			},
			{
				id:              "44",
				model:           "Qwen/Test",
				participantKeys: []string{"participant-1"},
			},
		},
	}
	collector := newGatewayMetricsCollectorWithHostConnections(g, fakeHostConnectionSnapshotter(nil))

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	families, err := registry.Gather()
	require.NoError(t, err)
	requireMetricGaugeValue(t, families, "devshard_gateway_participant_quarantine_state", map[string]string{"participant_key": "participant-1", "model": "Qwen/Test", "mode": "shadow"}, 1)
}

func TestGatewayMetricsCollectorIncludesModelCapacityScales(t *testing.T) {
	capacity := NewCapacityState()
	capacity.SetEscrowMembership("qwen", map[string]int{"host-q": 1})
	capacity.SetEscrowMembership("kimi", map[string]int{"host-k": 1})
	capacity.SetHostWeightViews(
		map[string]float64{"host-q": 40, "host-k": 50},
		map[string]float64{"host-q": 150, "host-k": 50},
		map[string]map[string]float64{
			"Qwen/Test": {"host-q": 40},
			"Kimi/Test": {"host-k": 50},
		},
		map[string]map[string]float64{
			"Qwen/Test": {"host-q": 150},
			"Kimi/Test": {"host-k": 50},
		},
	)
	g := &Gateway{
		capacity: capacity,
		runtimeOrder: []*devshardRuntime{
			{id: "qwen", model: "Qwen/Test"},
			{id: "kimi", model: "Kimi/Test"},
			{id: "runtime-only", model: "Runtime/Only"},
		},
	}
	collector := newGatewayMetricsCollectorWithHostConnections(g, fakeHostConnectionSnapshotter(nil))

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	families, err := registry.Gather()
	require.NoError(t, err)
	requireMetricGaugeValue(t, families, "devshard_gateway_capacity_scale_by_model", map[string]string{"model": "Qwen/Test"}, 40.0/150.0)
	requireMetricGaugeValue(t, families, "devshard_gateway_capacity_total_weight_by_model", map[string]string{"model": "Qwen/Test"}, 40)
	requireMetricGaugeValue(t, families, "devshard_gateway_capacity_baseline_weight_by_model", map[string]string{"model": "Qwen/Test"}, 150)
	requireMetricGaugeValue(t, families, "devshard_gateway_capacity_scale_by_model", map[string]string{"model": "Kimi/Test"}, 1)
	requireMetricGaugeValue(t, families, "devshard_gateway_capacity_total_weight_by_model", map[string]string{"model": "Kimi/Test"}, 50)
	requireMetricGaugeValue(t, families, "devshard_gateway_capacity_baseline_weight_by_model", map[string]string{"model": "Kimi/Test"}, 50)
	requireMetricGaugeValue(t, families, "devshard_gateway_capacity_scale_by_model", map[string]string{"model": "Runtime/Only"}, 90.0/200.0)
	requireMetricGaugeValue(t, families, "devshard_gateway_capacity_total_weight_by_model", map[string]string{"model": "Runtime/Only"}, 90)
	requireMetricGaugeValue(t, families, "devshard_gateway_capacity_baseline_weight_by_model", map[string]string{"model": "Runtime/Only"}, 200)
}

func TestParticipantLimiterRecordsQuarantineTransitions(t *testing.T) {
	m := NewDevshardMetrics()
	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.SetMetrics(m)

	limiter.ObserveResultWithBodyForModel("participant-1", "Qwen/Test", "/sessions/12/chat/completions", http.StatusServiceUnavailable, "")
	for i := 0; i < emptyStreamQuarantineThreshold; i++ {
		limiter.ObserveEmptyStreamForModel("participant-2", "Qwen/Test")
	}

	families, err := m.registry.Gather()
	require.NoError(t, err)
	requireMetricCounterValue(t, families, "devshard_gateway_participant_quarantine_transitions_total", map[string]string{"participant_key": "participant-1", "model": "Qwen/Test", "mode": "probe", "reason": "http_throttle_quarantine"}, 1)
	requireMetricCounterValue(t, families, "devshard_gateway_participant_quarantine_transitions_total", map[string]string{"participant_key": "participant-2", "model": "Qwen/Test", "mode": "shadow", "reason": "empty_stream_quarantine"}, 1)
}

func TestGatewayParticipantTimingMetricsRecordAddressAndModel(t *testing.T) {
	m := NewDevshardMetrics()
	now := time.Now()

	m.ObserveRequestSample("12", RequestSample{
		HostIdx:        1,
		ParticipantKey: "participant-1",
		Model:          "Qwen/Test",
		SendTime:       now,
		ReceiptTime:    now.Add(100 * time.Millisecond),
		FirstToken:     now.Add(300 * time.Millisecond),
		TotalTime:      900 * time.Millisecond,
		InputTokens:    10,
	})

	families, err := m.registry.Gather()
	require.NoError(t, err)
	labels := map[string]string{"participant_key": "participant-1", "model": "Qwen/Test"}
	requireMetricHistogramCount(t, families, "devshard_gateway_participant_receipt_seconds", labels, 1)
	requireMetricHistogramCount(t, families, "devshard_gateway_participant_first_content_seconds", labels, 1)
	requireMetricHistogramCount(t, families, "devshard_gateway_participant_prefill_seconds_per_input_token", labels, 1)
	requireMetricHistogramCount(t, families, "devshard_gateway_participant_total_attempt_seconds", labels, 1)
}

func TestGatewayAttemptMetricClassifiers(t *testing.T) {
	now := time.Now()

	emptyStreamAttempt := &inflight{}
	emptyStreamAttempt.setReceiptAt(now)
	errorStreamAttempt := &inflight{errorSource: "error.BadRequestError"}
	errorStreamAttempt.setReceiptAt(now)

	require.Equal(t, "empty_stream", gatewayAttemptFailureReason(emptyStreamAttempt, nil))
	require.Equal(t, "error_stream", gatewayAttemptFailureReason(errorStreamAttempt, nil))
	require.Equal(t, "eof_transport", gatewayAttemptFailureReason(&inflight{err: io.EOF}, nil))
	require.Equal(t, "phase_transition_aborted", gatewayAttemptFailureReason(&inflight{phaseTransitionAborted: true}, nil))

	require.Equal(t, "user_visible_winner", gatewayAttemptVisibility(&inflight{nonce: 7}, 7, true))
	require.Equal(t, "no_winner", gatewayAttemptVisibility(&inflight{nonce: 7, suspicious: true}, 7, true))
	require.Equal(t, "suppressed_loser", gatewayAttemptVisibility(&inflight{nonce: 8}, 7, true))
	require.Equal(t, "failed_not_finished", gatewayAttemptVisibility(&inflight{nonce: 8}, 0, false))
}

func requireMetricCounterValue(t *testing.T, families []*dto.MetricFamily, name string, labels map[string]string, want float64) {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metricLabelsMatch(metric, labels) {
				require.NotNil(t, metric.Counter)
				require.Equal(t, want, metric.Counter.GetValue())
				return
			}
		}
	}
	t.Fatalf("metric %s with labels %v not found", name, labels)
}

func requireMetricHistogramCount(t *testing.T, families []*dto.MetricFamily, name string, labels map[string]string, want uint64) {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metricLabelsMatch(metric, labels) {
				require.NotNil(t, metric.Histogram)
				require.Equal(t, want, metric.Histogram.GetSampleCount())
				return
			}
		}
	}
	t.Fatalf("histogram %s with labels %v not found", name, labels)
}
