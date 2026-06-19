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

func TestGatewayAttemptMetricClassifiers(t *testing.T) {
	now := time.Now()

	require.Equal(t, "empty_stream", gatewayAttemptFailureReason(&inflight{receiptTime: now}, nil))
	require.Equal(t, "error_stream", gatewayAttemptFailureReason(&inflight{receiptTime: now, errorSource: "error.BadRequestError"}, nil))
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
