package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"devshard/transport"
)

type DevshardMetrics struct {
	registry *prometheus.Registry
	handler  http.Handler

	httpRequests               *prometheus.CounterVec
	httpRequestDuration        *prometheus.HistogramVec
	gatewayLimitRejections     *prometheus.CounterVec
	participantLimitRejections *prometheus.CounterVec
	participantTransportErrors *prometheus.CounterVec
	speculativeDecisions       *prometheus.CounterVec
	speculativeAttempts        *prometheus.CounterVec
	inferenceTimeouts          *prometheus.CounterVec
	pickerChoices              *prometheus.CounterVec
	hostReceiptSeconds         *prometheus.HistogramVec
	hostFirstTokenSeconds      *prometheus.HistogramVec
	hostCTTFLSecondsPerToken   *prometheus.HistogramVec
	hostTotalSeconds           *prometheus.HistogramVec
	participantReceiptSeconds  *prometheus.HistogramVec
	participantFirstContent    *prometheus.HistogramVec
	participantPrefillPerToken *prometheus.HistogramVec
	participantTotalSeconds    *prometheus.HistogramVec

	gatewayRequests       *prometheus.CounterVec
	criticalUserFailures  *prometheus.CounterVec
	hiddenFailures        *prometheus.CounterVec
	userVisibleWins       *prometheus.CounterVec
	slotDecisions         *prometheus.CounterVec
	attemptsStarted       *prometheus.CounterVec
	attemptsTerminal      *prometheus.CounterVec
	attemptFailures       *prometheus.CounterVec
	quarantineTransitions *prometheus.CounterVec
	noWinnerAttempts      *prometheus.CounterVec
	timeoutActions        *prometheus.CounterVec
}

type GatewaySlotDecisionMetric struct {
	ParticipantKey string
	Model          string
	EscrowID       string
	Decision       string
	Reason         string
	QuarantineMode string
}

type GatewayAttemptStartMetric struct {
	ParticipantKey string
	Model          string
	Role           string
	Reason         string
	QuarantineMode string
}

type GatewayAttemptTerminalMetric struct {
	ParticipantKey string
	Model          string
	Role           string
	Outcome        string
	Visibility     string
}

type GatewayAttemptFailureMetric struct {
	ParticipantKey string
	Model          string
	Role           string
	Reason         string
	Visibility     string
}

type GatewayTimeoutActionMetric struct {
	ParticipantKey string
	Model          string
	Kind           string
	Action         string
	Reason         string
}

func NewDevshardMetrics() *DevshardMetrics {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &DevshardMetrics{
		registry: registry,
		httpRequests: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_http_requests_total",
				Help: "Total HTTP requests handled by the devshard gateway.",
			},
			[]string{"path", "method", "status"},
		),
		httpRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "devshard_http_request_duration_seconds",
				Help:    "End-to-end HTTP request duration for the devshard gateway.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"path", "method"},
		),
		gatewayLimitRejections: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_gateway_limit_rejections_total",
				Help: "Total gateway limiter rejections by reason.",
			},
			[]string{"reason"},
		),
		participantLimitRejections: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_gateway_participant_limit_rejections_total",
				Help: "Total participant-budget rejections by participant, model, and routing scope.",
			},
			[]string{"participant_key", "model", "scope"},
		),
		participantTransportErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_gateway_participant_transport_errors_total",
				Help: "Total participant-bound transport request errors by participant, model, request kind, and upstream status.",
			},
			[]string{"participant_key", "model", "path_kind", "status"},
		),
		speculativeDecisions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_speculative_decisions_total",
				Help: "Total speculative execution decisions by reason.",
			},
			[]string{"reason"},
		),
		speculativeAttempts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_speculative_attempt_starts_total",
				Help: "Total speculative extra inference attempt starts by reason.",
			},
			[]string{"reason"},
		),
		inferenceTimeouts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_inference_timeouts_total",
				Help: "Total inference timeout handling attempts by reason.",
			},
			[]string{"reason"},
		),
		pickerChoices: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_gateway_picker_choice_total",
				Help: "Total escrow selections by the capacity-aware gateway picker.",
			},
			[]string{"devshard_id", "model"},
		),
		hostReceiptSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "devshard_host_receipt_seconds",
				Help:    "Time from inference send until host receipt confirmation.",
				Buckets: prometheus.ExponentialBuckets(0.01, 2, 12),
			},
			[]string{"devshard_id", "host_idx"},
		),
		hostFirstTokenSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "devshard_host_first_token_seconds",
				Help:    "Time from inference send until first streamed token.",
				Buckets: prometheus.ExponentialBuckets(0.01, 2, 12),
			},
			[]string{"devshard_id", "host_idx"},
		),
		hostCTTFLSecondsPerToken: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "devshard_host_cttfl_seconds_per_input_token",
				Help:    "Prefill time per input token, computed from receipt to first token.",
				Buckets: prometheus.ExponentialBuckets(0.0001, 2, 12),
			},
			[]string{"devshard_id", "host_idx"},
		),
		hostTotalSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "devshard_host_total_time_seconds",
				Help:    "Total inference time observed per host.",
				Buckets: prometheus.ExponentialBuckets(0.01, 2, 12),
			},
			[]string{"devshard_id", "host_idx"},
		),
		participantReceiptSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "devshard_gateway_participant_receipt_seconds",
				Help:    "Time from gateway inference send until receipt confirmation by participant and model.",
				Buckets: prometheus.ExponentialBuckets(0.01, 2, 12),
			},
			[]string{"participant_key", "model"},
		),
		participantFirstContent: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "devshard_gateway_participant_first_content_seconds",
				Help:    "Time from gateway inference send until first content by participant and model.",
				Buckets: prometheus.ExponentialBuckets(0.01, 2, 12),
			},
			[]string{"participant_key", "model"},
		),
		participantPrefillPerToken: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "devshard_gateway_participant_prefill_seconds_per_input_token",
				Help:    "Time from receipt until first content divided by input tokens, by participant and model.",
				Buckets: prometheus.ExponentialBuckets(0.0001, 2, 12),
			},
			[]string{"participant_key", "model"},
		),
		participantTotalSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "devshard_gateway_participant_total_attempt_seconds",
				Help:    "Total inference attempt time observed by the gateway, by participant and model.",
				Buckets: prometheus.ExponentialBuckets(0.01, 2, 12),
			},
			[]string{"participant_key", "model"},
		),
		gatewayRequests: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_gateway_requests_total",
				Help: "Total gateway chat requests by model, user-visible outcome, and bounded reason.",
			},
			[]string{"model", "outcome", "reason"},
		),
		criticalUserFailures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_gateway_critical_user_failures_total",
				Help: "Total critical user-visible gateway failures by model and bounded reason.",
			},
			[]string{"model", "reason"},
		),
		hiddenFailures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_gateway_user_requests_with_hidden_failure_total",
				Help: "Total successful user requests that hid gateway-visible participant or policy failures.",
			},
			[]string{"model", "severity", "reason"},
		),
		userVisibleWins: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_gateway_user_visible_wins_total",
				Help: "Total user-visible winning responses by participant and model.",
			},
			[]string{"participant_key", "model"},
		),
		slotDecisions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_gateway_slot_decisions_total",
				Help: "Total gateway slot decisions by participant, model, escrow, decision, reason, and quarantine mode.",
			},
			[]string{"participant_key", "model", "escrow_id", "decision", "reason", "quarantine_mode"},
		),
		attemptsStarted: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_gateway_attempts_started_total",
				Help: "Total real gateway attempts started by participant, model, role, reason, and quarantine mode.",
			},
			[]string{"participant_key", "model", "role", "reason", "quarantine_mode"},
		),
		attemptsTerminal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_gateway_attempts_terminal_total",
				Help: "Total real gateway attempts by terminal outcome and visibility.",
			},
			[]string{"participant_key", "model", "role", "outcome", "visibility"},
		),
		attemptFailures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_gateway_attempt_failures_total",
				Help: "Total failed real gateway attempts by bounded failure reason and visibility.",
			},
			[]string{"participant_key", "model", "role", "reason", "visibility"},
		),
		quarantineTransitions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_gateway_participant_quarantine_transitions_total",
				Help: "Total participant quarantine and no-winner state transitions by participant, model, mode, and reason.",
			},
			[]string{"participant_key", "model", "mode", "reason"},
		),
		noWinnerAttempts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_gateway_no_winner_attempts_total",
				Help: "Total real no-winner attempts by participant, model, reason, and quarantine mode.",
			},
			[]string{"participant_key", "model", "reason", "quarantine_mode"},
		),
		timeoutActions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_gateway_timeout_actions_total",
				Help: "Total gateway timeout actions by participant, model, timeout kind, action, and reason.",
			},
			[]string{"participant_key", "model", "kind", "action", "reason"},
		),
	}

	registry.MustRegister(
		m.httpRequests,
		m.httpRequestDuration,
		m.gatewayLimitRejections,
		m.participantLimitRejections,
		m.participantTransportErrors,
		m.speculativeDecisions,
		m.speculativeAttempts,
		m.inferenceTimeouts,
		m.pickerChoices,
		m.hostReceiptSeconds,
		m.hostFirstTokenSeconds,
		m.hostCTTFLSecondsPerToken,
		m.hostTotalSeconds,
		m.participantReceiptSeconds,
		m.participantFirstContent,
		m.participantPrefillPerToken,
		m.participantTotalSeconds,
		m.gatewayRequests,
		m.criticalUserFailures,
		m.hiddenFailures,
		m.userVisibleWins,
		m.slotDecisions,
		m.attemptsStarted,
		m.attemptsTerminal,
		m.attemptFailures,
		m.quarantineTransitions,
		m.noWinnerAttempts,
		m.timeoutActions,
	)

	m.handler = promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	return m
}

func (m *DevshardMetrics) AttachGateway(g *Gateway) {
	if m == nil || g == nil {
		return
	}
	m.registry.MustRegister(newGatewayMetricsCollector(g))
}

func (m *DevshardMetrics) Handler() http.Handler {
	if m == nil {
		return http.NotFoundHandler()
	}
	return m.handler
}

func (m *DevshardMetrics) Wrap(next http.Handler) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := normalizeMetricsPath(r.URL.Path)
		if path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		recorder := &metricsResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)

		method := r.Method
		status := strconv.Itoa(recorder.status)
		m.httpRequests.WithLabelValues(path, method, status).Inc()
		m.httpRequestDuration.WithLabelValues(path, method).Observe(time.Since(start).Seconds())
	})
}

func (m *DevshardMetrics) RecordLimitRejection(reason string) {
	if m == nil {
		return
	}
	m.gatewayLimitRejections.WithLabelValues(reason).Inc()
}

func (m *DevshardMetrics) RecordParticipantLimitRejection(participantKey, model, scope string) {
	if m == nil {
		return
	}
	m.participantLimitRejections.WithLabelValues(
		metricLabel(participantKey, "unknown"),
		metricLabel(model, "unknown"),
		metricLabel(scope, "unknown"),
	).Inc()
}

func (m *DevshardMetrics) RecordParticipantTransportError(participantKey, model, pathKind string, statusCode int) {
	if m == nil {
		return
	}
	m.participantTransportErrors.WithLabelValues(
		metricLabel(participantKey, "unknown"),
		metricLabel(model, "unknown"),
		metricLabel(pathKind, "unknown"),
		strconv.Itoa(statusCode),
	).Inc()
}

func (m *DevshardMetrics) RecordSpeculativeDecision(reason string) {
	if m == nil {
		return
	}
	m.speculativeDecisions.WithLabelValues(reason).Inc()
}

func (m *DevshardMetrics) RecordSpeculativeAttemptStart(reason string) {
	if m == nil {
		return
	}
	m.speculativeAttempts.WithLabelValues(reason).Inc()
}

func (m *DevshardMetrics) RecordInferenceTimeout(reason string) {
	if m == nil {
		return
	}
	m.inferenceTimeouts.WithLabelValues(reason).Inc()
}

func (m *DevshardMetrics) RecordPickerChoice(devshardID, model string) {
	if m == nil {
		return
	}
	m.pickerChoices.WithLabelValues(devshardID, model).Inc()
}

func (m *DevshardMetrics) RecordGatewayRequest(model, outcome, reason string) {
	if m == nil {
		return
	}
	m.gatewayRequests.WithLabelValues(metricLabel(model, "unknown"), metricLabel(outcome, "unknown"), metricLabel(reason, "none")).Inc()
}

func (m *DevshardMetrics) RecordCriticalUserFailure(model, reason string) {
	if m == nil {
		return
	}
	m.criticalUserFailures.WithLabelValues(metricLabel(model, "unknown"), metricLabel(reason, "unknown")).Inc()
}

func (m *DevshardMetrics) RecordGatewayHiddenFailure(model, severity, reason string) {
	if m == nil {
		return
	}
	m.hiddenFailures.WithLabelValues(metricLabel(model, "unknown"), metricLabel(severity, "unknown"), metricLabel(reason, "unknown")).Inc()
}

func (m *DevshardMetrics) RecordGatewayUserVisibleWin(participantKey, model string) {
	if m == nil {
		return
	}
	m.userVisibleWins.WithLabelValues(metricLabel(participantKey, "unknown"), metricLabel(model, "unknown")).Inc()
}

func (m *DevshardMetrics) RecordGatewaySlotDecision(decision GatewaySlotDecisionMetric) {
	if m == nil {
		return
	}
	m.slotDecisions.WithLabelValues(
		metricLabel(decision.ParticipantKey, "unknown"),
		metricLabel(decision.Model, "unknown"),
		metricLabel(decision.EscrowID, "unknown"),
		metricLabel(decision.Decision, "unknown"),
		metricLabel(decision.Reason, "unknown"),
		metricLabel(decision.QuarantineMode, "none"),
	).Inc()
}

func (m *DevshardMetrics) RecordGatewayAttemptStarted(start GatewayAttemptStartMetric) {
	if m == nil {
		return
	}
	m.attemptsStarted.WithLabelValues(
		metricLabel(start.ParticipantKey, "unknown"),
		metricLabel(start.Model, "unknown"),
		metricLabel(start.Role, "unknown"),
		metricLabel(start.Reason, "none"),
		metricLabel(start.QuarantineMode, "none"),
	).Inc()
}

func (m *DevshardMetrics) RecordGatewayAttemptTerminal(terminal GatewayAttemptTerminalMetric) {
	if m == nil {
		return
	}
	m.attemptsTerminal.WithLabelValues(
		metricLabel(terminal.ParticipantKey, "unknown"),
		metricLabel(terminal.Model, "unknown"),
		metricLabel(terminal.Role, "unknown"),
		metricLabel(terminal.Outcome, "unknown"),
		metricLabel(terminal.Visibility, "unknown"),
	).Inc()
}

func (m *DevshardMetrics) RecordGatewayAttemptFailure(failure GatewayAttemptFailureMetric) {
	if m == nil {
		return
	}
	m.attemptFailures.WithLabelValues(
		metricLabel(failure.ParticipantKey, "unknown"),
		metricLabel(failure.Model, "unknown"),
		metricLabel(failure.Role, "unknown"),
		metricLabel(failure.Reason, "unknown"),
		metricLabel(failure.Visibility, "unknown"),
	).Inc()
}

func (m *DevshardMetrics) RecordGatewayQuarantineTransition(participantKey, model, mode, reason string) {
	if m == nil {
		return
	}
	m.quarantineTransitions.WithLabelValues(
		metricLabel(participantKey, "unknown"),
		metricLabel(model, "all"),
		metricLabel(mode, "none"),
		metricLabel(reason, "unknown"),
	).Inc()
}

func (m *DevshardMetrics) RecordGatewayNoWinnerAttempt(participantKey, model, reason, quarantineMode string) {
	if m == nil {
		return
	}
	m.noWinnerAttempts.WithLabelValues(
		metricLabel(participantKey, "unknown"),
		metricLabel(model, "unknown"),
		metricLabel(reason, "unknown"),
		metricLabel(quarantineMode, "none"),
	).Inc()
}

func (m *DevshardMetrics) RecordGatewayTimeoutAction(action GatewayTimeoutActionMetric) {
	if m == nil {
		return
	}
	m.timeoutActions.WithLabelValues(
		metricLabel(action.ParticipantKey, "unknown"),
		metricLabel(action.Model, "unknown"),
		metricLabel(action.Kind, "unknown"),
		metricLabel(action.Action, "unknown"),
		metricLabel(action.Reason, "none"),
	).Inc()
}

func (m *DevshardMetrics) ObserveRequestSample(devshardID string, sample RequestSample) {
	if m == nil {
		return
	}

	labels := []string{devshardID, strconv.Itoa(sample.HostIdx)}
	participantLabels := []string{
		metricLabel(sample.ParticipantKey, "unknown"),
		metricLabel(sample.Model, "unknown"),
	}
	if receiptSeconds := sample.ReceiptMs() / 1000; receiptSeconds > 0 {
		m.hostReceiptSeconds.WithLabelValues(labels...).Observe(receiptSeconds)
		m.participantReceiptSeconds.WithLabelValues(participantLabels...).Observe(receiptSeconds)
	}
	if !sample.SendTime.IsZero() && !sample.FirstToken.IsZero() {
		firstContentSeconds := sample.FirstToken.Sub(sample.SendTime).Seconds()
		m.hostFirstTokenSeconds.WithLabelValues(labels...).Observe(firstContentSeconds)
		m.participantFirstContent.WithLabelValues(participantLabels...).Observe(firstContentSeconds)
	}
	if cttfl := sample.CTTFL() / 1000; cttfl > 0 {
		m.hostCTTFLSecondsPerToken.WithLabelValues(labels...).Observe(cttfl)
		m.participantPrefillPerToken.WithLabelValues(participantLabels...).Observe(cttfl)
	}
	if sample.TotalTime > 0 {
		m.hostTotalSeconds.WithLabelValues(labels...).Observe(sample.TotalTime.Seconds())
		m.participantTotalSeconds.WithLabelValues(participantLabels...).Observe(sample.TotalTime.Seconds())
	}
}

func metricLabel(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	fallback = strings.TrimSpace(fallback)
	if fallback != "" {
		return fallback
	}
	return "unknown"
}

type gatewayMetricsCollector struct {
	gateway         *Gateway
	hostConnections hostConnectionSnapshotter

	inflightRequestsDesc           *prometheus.Desc
	inflightTokensDesc             *prometheus.Desc
	effectiveMaxConcurrentDesc     *prometheus.Desc
	effectiveMaxInputTokensDesc    *prometheus.Desc
	capacityScaleDesc              *prometheus.Desc
	capacityTotalDesc              *prometheus.Desc
	capacityBaselineDesc           *prometheus.Desc
	capacityScaleByModelDesc       *prometheus.Desc
	capacityTotalByModelDesc       *prometheus.Desc
	capacityBaselineByModelDesc    *prometheus.Desc
	escrowWeightDesc               *prometheus.Desc
	runtimeActiveDesc              *prometheus.Desc
	runtimeRequestsDesc            *prometheus.Desc
	runtimeReservedDesc            *prometheus.Desc
	participantExhaustedDesc       *prometheus.Desc
	participantTrackedDesc         *prometheus.Desc
	participantQuarantineStateDesc *prometheus.Desc
	escrowParticipantLimitedDesc   *prometheus.Desc
	escrowBlockedParticipantsDesc  *prometheus.Desc
	hostOpenDesc                   *prometheus.Desc
	hostStateDesc                  *prometheus.Desc
}

func newGatewayMetricsCollector(gateway *Gateway) *gatewayMetricsCollector {
	return newGatewayMetricsCollectorWithHostConnections(gateway, transport.DefaultHostConnectionTracker())
}

type hostConnectionSnapshotter interface {
	Snapshots() []transport.HostConnectionSnapshot
}

func newGatewayMetricsCollectorWithHostConnections(gateway *Gateway, hostConnections hostConnectionSnapshotter) *gatewayMetricsCollector {
	return &gatewayMetricsCollector{
		gateway:         gateway,
		hostConnections: hostConnections,
		inflightRequestsDesc: prometheus.NewDesc(
			"devshard_gateway_inflight_requests",
			"Current number of in-flight requests tracked by the gateway limiter.",
			nil,
			nil,
		),
		inflightTokensDesc: prometheus.NewDesc(
			"devshard_gateway_inflight_input_tokens",
			"Current number of in-flight input tokens tracked by the gateway limiter.",
			nil,
			nil,
		),
		effectiveMaxConcurrentDesc: prometheus.NewDesc(
			"devshard_gateway_effective_max_concurrent_requests",
			"Currently enforced concurrent-request cap after capacity scaling.",
			nil,
			nil,
		),
		effectiveMaxInputTokensDesc: prometheus.NewDesc(
			"devshard_gateway_effective_max_input_tokens_in_flight",
			"Currently enforced input-token cap after capacity scaling.",
			nil,
			nil,
		),
		capacityScaleDesc: prometheus.NewDesc(
			"devshard_gateway_capacity_scale",
			"Ratio W_tot / W_ref currently applied to gateway-wide caps (1.0 = no scaling).",
			nil,
			nil,
		),
		capacityTotalDesc: prometheus.NewDesc(
			"devshard_gateway_capacity_total_weight",
			"Current gateway-wide effective host weight (W_tot).",
			nil,
			nil,
		),
		capacityBaselineDesc: prometheus.NewDesc(
			"devshard_gateway_capacity_baseline_weight",
			"Baseline gateway-wide host weight (W_ref) snapshotted during steady-state Inference.",
			nil,
			nil,
		),
		capacityScaleByModelDesc: prometheus.NewDesc(
			"devshard_gateway_capacity_scale_by_model",
			"Ratio W_tot(model) / W_ref(model) for each model (1.0 = full model capacity).",
			[]string{"model"},
			nil,
		),
		capacityTotalByModelDesc: prometheus.NewDesc(
			"devshard_gateway_capacity_total_weight_by_model",
			"Current gateway-wide effective host weight (W_tot) for each model.",
			[]string{"model"},
			nil,
		),
		capacityBaselineByModelDesc: prometheus.NewDesc(
			"devshard_gateway_capacity_baseline_weight_by_model",
			"Baseline gateway-wide host weight (W_ref) for each model.",
			[]string{"model"},
			nil,
		),
		escrowWeightDesc: prometheus.NewDesc(
			"devshard_gateway_escrow_weight",
			"Per-escrow effective weight W(e) used by the capacity-aware picker.",
			[]string{"devshard_id"},
			nil,
		),
		runtimeActiveDesc: prometheus.NewDesc(
			"devshard_runtime_active",
			"Whether a devshard runtime is active.",
			[]string{"devshard_id", "model"},
			nil,
		),
		runtimeRequestsDesc: prometheus.NewDesc(
			"devshard_runtime_active_requests",
			"Current number of active requests assigned to a devshard runtime.",
			[]string{"devshard_id", "model"},
			nil,
		),
		runtimeReservedDesc: prometheus.NewDesc(
			"devshard_runtime_reserved_tokens",
			"Current number of reserved input tokens assigned to a devshard runtime.",
			[]string{"devshard_id", "model"},
			nil,
		),
		participantExhaustedDesc: prometheus.NewDesc(
			"devshard_gateway_participants_exhausted",
			"Current number of reactively tracked participants that are currently blocked (tokens < 1).",
			nil,
			nil,
		),
		participantTrackedDesc: prometheus.NewDesc(
			"devshard_gateway_participants_tracked",
			"Current number of participants in reactive throttle tracking (entered after first 429/503).",
			nil,
			nil,
		),
		participantQuarantineStateDesc: prometheus.NewDesc(
			"devshard_gateway_participant_quarantine_state",
			"Current participant quarantine or no-winner state by participant and model.",
			[]string{"participant_key", "model", "mode"},
			nil,
		),
		escrowParticipantLimitedDesc: prometheus.NewDesc(
			"devshard_gateway_escrow_participant_limited",
			"Whether an escrow is currently blocked by at least one participant budget.",
			[]string{"devshard_id", "model"},
			nil,
		),
		escrowBlockedParticipantsDesc: prometheus.NewDesc(
			"devshard_gateway_escrow_blocked_participants",
			"Current number of blocked participants within an escrow.",
			[]string{"devshard_id", "model"},
			nil,
		),
		hostOpenDesc: prometheus.NewDesc(
			"devshard_host_transport_open_connections",
			"Current number of open host transport connections by remote address.",
			[]string{"address"},
			nil,
		),
		hostStateDesc: prometheus.NewDesc(
			"devshard_host_transport_connections",
			"Current number of host transport connections by remote address and lifecycle state.",
			[]string{"address", "state"},
			nil,
		),
	}
}

func (c *gatewayMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.inflightRequestsDesc
	ch <- c.inflightTokensDesc
	ch <- c.effectiveMaxConcurrentDesc
	ch <- c.effectiveMaxInputTokensDesc
	ch <- c.capacityScaleDesc
	ch <- c.capacityTotalDesc
	ch <- c.capacityBaselineDesc
	ch <- c.capacityScaleByModelDesc
	ch <- c.capacityTotalByModelDesc
	ch <- c.capacityBaselineByModelDesc
	ch <- c.escrowWeightDesc
	ch <- c.runtimeActiveDesc
	ch <- c.runtimeRequestsDesc
	ch <- c.runtimeReservedDesc
	ch <- c.participantExhaustedDesc
	ch <- c.participantTrackedDesc
	ch <- c.participantQuarantineStateDesc
	ch <- c.escrowParticipantLimitedDesc
	ch <- c.escrowBlockedParticipantsDesc
	ch <- c.hostOpenDesc
	ch <- c.hostStateDesc
}

func (c *gatewayMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	if c.gateway == nil {
		return
	}

	c.gateway.mu.Lock()
	runtimes := append([]*devshardRuntime(nil), c.gateway.runtimeOrder...)
	c.gateway.mu.Unlock()

	if c.gateway.limiter != nil {
		snapshot := c.gateway.limiter.Snapshot()
		ch <- prometheus.MustNewConstMetric(c.inflightRequestsDesc, prometheus.GaugeValue, float64(snapshot.InFlightRequests))
		ch <- prometheus.MustNewConstMetric(c.inflightTokensDesc, prometheus.GaugeValue, float64(snapshot.InFlightInputTokens))
		ch <- prometheus.MustNewConstMetric(c.effectiveMaxConcurrentDesc, prometheus.GaugeValue, float64(snapshot.EffectiveMaxConcurrent))
		ch <- prometheus.MustNewConstMetric(c.effectiveMaxInputTokensDesc, prometheus.GaugeValue, float64(snapshot.EffectiveMaxInputTokens))
		ch <- prometheus.MustNewConstMetric(c.capacityScaleDesc, prometheus.GaugeValue, snapshot.ScaleFactor)
	}
	if c.gateway.capacity != nil {
		capSnap := c.gateway.capacity.Snapshot()
		ch <- prometheus.MustNewConstMetric(c.capacityTotalDesc, prometheus.GaugeValue, capSnap.TotalWeight)
		ch <- prometheus.MustNewConstMetric(c.capacityBaselineDesc, prometheus.GaugeValue, capSnap.BaselineWeight)
		for id, w := range capSnap.EscrowWeights {
			ch <- prometheus.MustNewConstMetric(c.escrowWeightDesc, prometheus.GaugeValue, w, id)
		}
		for _, model := range capacityMetricModels(c.gateway.capacity, runtimes) {
			ch <- prometheus.MustNewConstMetric(c.capacityScaleByModelDesc, prometheus.GaugeValue, c.gateway.capacity.ScaleFactorForModel(model), model)
			ch <- prometheus.MustNewConstMetric(c.capacityTotalByModelDesc, prometheus.GaugeValue, c.gateway.capacity.TotalWeightForModel(model), model)
			ch <- prometheus.MustNewConstMetric(c.capacityBaselineByModelDesc, prometheus.GaugeValue, c.gateway.capacity.BaselineWeightForModel(model), model)
		}
	}
	if c.gateway.participantLimiter != nil {
		ch <- prometheus.MustNewConstMetric(
			c.participantExhaustedDesc,
			prometheus.GaugeValue,
			float64(c.gateway.participantLimiter.ExhaustedCount()),
		)
		ch <- prometheus.MustNewConstMetric(
			c.participantTrackedDesc,
			prometheus.GaugeValue,
			float64(c.gateway.participantLimiter.TrackedCount()),
		)
	}

	emittedParticipantStates := make(map[string]struct{})
	for _, rt := range runtimes {
		active := 0.0
		if rt.active.Load() {
			active = 1
		}
		labels := []string{rt.id, rt.model}
		ch <- prometheus.MustNewConstMetric(c.runtimeActiveDesc, prometheus.GaugeValue, active, labels...)
		ch <- prometheus.MustNewConstMetric(c.runtimeRequestsDesc, prometheus.GaugeValue, float64(rt.activeUserRequests.Load()), labels...)
		ch <- prometheus.MustNewConstMetric(c.runtimeReservedDesc, prometheus.GaugeValue, float64(rt.reservedTokens.Load()), labels...)
		blocked := 0
		if c.gateway.participantLimiter != nil {
			blocked = len(c.gateway.participantLimiter.BlockedParticipants(rt.participantKeys))
			for participantKey, snapshot := range c.gateway.participantLimiter.Snapshot(rt.participantKeys) {
				if !participantSnapshotAppliesToModel(snapshot, rt.model) {
					continue
				}
				model := metricLabel(rt.model, "unknown")
				mode := participantSnapshotMode(snapshot)
				stateKey := participantKey + "\x00" + model + "\x00" + mode
				if _, ok := emittedParticipantStates[stateKey]; ok {
					continue
				}
				emittedParticipantStates[stateKey] = struct{}{}
				ch <- prometheus.MustNewConstMetric(
					c.participantQuarantineStateDesc,
					prometheus.GaugeValue,
					1,
					participantKey,
					model,
					mode,
				)
			}
		}
		limited := 0.0
		if blocked > 0 {
			limited = 1
		}
		ch <- prometheus.MustNewConstMetric(c.escrowParticipantLimitedDesc, prometheus.GaugeValue, limited, labels...)
		ch <- prometheus.MustNewConstMetric(c.escrowBlockedParticipantsDesc, prometheus.GaugeValue, float64(blocked), labels...)
	}

	if c.hostConnections == nil {
		return
	}
	for _, snapshot := range c.hostConnections.Snapshots() {
		ch <- prometheus.MustNewConstMetric(c.hostOpenDesc, prometheus.GaugeValue, float64(snapshot.OpenTotal), snapshot.Address)
		ch <- prometheus.MustNewConstMetric(c.hostStateDesc, prometheus.GaugeValue, float64(snapshot.Active), snapshot.Address, "active")
		ch <- prometheus.MustNewConstMetric(c.hostStateDesc, prometheus.GaugeValue, float64(snapshot.Idle), snapshot.Address, "idle")
		ch <- prometheus.MustNewConstMetric(c.hostStateDesc, prometheus.GaugeValue, float64(snapshot.HoldAfterClose), snapshot.Address, "hold_after_close")
	}
}

func participantSnapshotAppliesToModel(snapshot ParticipantThrottleSnapshot, model string) bool {
	if len(snapshot.ModelIDs) == 0 {
		return true
	}
	model = normalizeModelID(model)
	if model == "" {
		return true
	}
	for _, modelID := range snapshot.ModelIDs {
		if normalizeModelID(modelID) == model {
			return true
		}
	}
	return false
}

func capacityMetricModels(capacity *CapacityState, runtimes []*devshardRuntime) []string {
	seen := make(map[string]struct{})
	if capacity != nil {
		for _, model := range capacity.Models() {
			model = strings.TrimSpace(model)
			if model != "" {
				seen[model] = struct{}{}
			}
		}
	}
	for _, rt := range runtimes {
		if rt == nil {
			continue
		}
		model := strings.TrimSpace(rt.model)
		if model != "" {
			seen[model] = struct{}{}
		}
	}
	models := make([]string, 0, len(seen))
	for model := range seen {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

func participantSnapshotMode(snapshot ParticipantThrottleSnapshot) string {
	switch {
	case snapshot.ProbeQuarantined:
		return "probe"
	case snapshot.ShadowQuarantined:
		return "shadow"
	case snapshot.Probationary:
		return "probation"
	default:
		return "none"
	}
}

type nonceFinishedChecker interface {
	IsNonceFinished(uint64) bool
}

func gatewayAttemptFailureReason(inf *inflight, session nonceFinishedChecker) string {
	if inf == nil {
		return "unknown"
	}
	switch {
	case inf.phaseTransitionAborted:
		return "phase_transition_aborted"
	case isErrorStreamAttempt(inf):
		return "error_stream"
	case isEmptyStreamAttempt(inf):
		return "empty_stream"
	}
	if inf.err != nil {
		var upstreamErr *transport.UpstreamStatusError
		switch {
		case errors.As(inf.err, &upstreamErr):
			return gatewayHTTPFailureReason(upstreamErr.StatusCode)
		case errors.Is(inf.err, transport.ErrSSEStreamTruncated):
			return "sse_truncated"
		case errors.Is(inf.err, io.EOF), errors.Is(inf.err, io.ErrUnexpectedEOF), strings.Contains(strings.ToLower(inf.err.Error()), "eof"):
			return "eof_transport"
		case errors.Is(inf.err, context.Canceled), errors.Is(inf.err, context.DeadlineExceeded):
			return "client_cancelled"
		default:
			return "transport_error"
		}
	}
	if !inf.hasReceipt() {
		return "no_receipt"
	}
	if session != nil && !session.IsNonceFinished(inf.nonce) {
		return "not_finished"
	}
	return "unknown"
}

func gatewayHTTPFailureReason(statusCode int) string {
	switch statusCode {
	case http.StatusTooManyRequests:
		return "http_429"
	case http.StatusServiceUnavailable:
		return "http_503"
	case http.StatusForbidden:
		return "http_forbidden"
	case http.StatusNotFound:
		return "http_not_found"
	case http.StatusUnauthorized:
		return "http_timestamp_drift"
	default:
		if statusCode >= 400 {
			return "http_error"
		}
		return "transport_error"
	}
}

func gatewayAttemptVisibility(inf *inflight, winnerNonce uint64, successful bool) string {
	if inf == nil {
		return "unknown"
	}
	if successful && winnerNonce != 0 && inf.nonce == winnerNonce {
		if inf.suspicious {
			return "no_winner"
		}
		return "user_visible_winner"
	}
	if successful {
		return "suppressed_loser"
	}
	return "failed_not_finished"
}

type metricsResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *metricsResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Unwrap exposes the inner ResponseWriter so that http.NewResponseController
// can reach capabilities the wrapper itself does not implement (Flusher,
// Hijacker, CloseNotifier, ...). Without this, SSE flushes from downstream
// handlers silently no-op because *metricsResponseWriter does not satisfy
// http.Flusher even when the underlying writer does.
func (w *metricsResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func normalizeMetricsPath(path string) string {
	switch {
	case path == "":
		return "/"
	case path == "/metrics":
		return path
	case strings.HasPrefix(path, "/devshard/"):
		if devshardID, inner, ok := parseDevshardPath(path); ok && devshardID != "" {
			return "/devshard/{id}" + inner
		}
		return "/devshard/{id}"
	case strings.HasPrefix(path, "/v1/admin/devshards/"):
		trimmed := strings.Trim(strings.TrimPrefix(path, "/v1/admin/devshards/"), "/")
		parts := strings.Split(trimmed, "/")
		if len(parts) >= 2 && parts[0] != "" {
			return "/v1/admin/devshards/{id}/" + parts[1]
		}
		if len(parts) >= 1 && parts[0] != "" {
			return "/v1/admin/devshards/{id}"
		}
		return "/v1/admin/devshards"
	default:
		return path
	}
}

func limiterReasonLabel(err error) string {
	if err == nil {
		return "unknown"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "concurrent requests"):
		return "max_concurrent_requests"
	case strings.Contains(msg, "input tokens in flight"):
		return "max_input_tokens_in_flight"
	default:
		return "unknown"
	}
}
