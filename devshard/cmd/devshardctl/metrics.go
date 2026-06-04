package main

import (
	"net/http"
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
				Help: "Total participant-budget rejections by routing scope.",
			},
			[]string{"scope"},
		),
		participantTransportErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshard_gateway_participant_transport_errors_total",
				Help: "Total participant-bound transport request errors by request kind and upstream status.",
			},
			[]string{"path_kind", "status"},
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

func (m *DevshardMetrics) RecordParticipantLimitRejection(scope string) {
	if m == nil {
		return
	}
	m.participantLimitRejections.WithLabelValues(scope).Inc()
}

func (m *DevshardMetrics) RecordParticipantTransportError(pathKind string, statusCode int) {
	if m == nil {
		return
	}
	m.participantTransportErrors.WithLabelValues(pathKind, strconv.Itoa(statusCode)).Inc()
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

func (m *DevshardMetrics) ObserveRequestSample(devshardID string, sample RequestSample) {
	if m == nil {
		return
	}

	labels := []string{devshardID, strconv.Itoa(sample.HostIdx)}
	if receiptSeconds := sample.ReceiptMs() / 1000; receiptSeconds > 0 {
		m.hostReceiptSeconds.WithLabelValues(labels...).Observe(receiptSeconds)
	}
	if !sample.SendTime.IsZero() && !sample.FirstToken.IsZero() {
		m.hostFirstTokenSeconds.WithLabelValues(labels...).Observe(sample.FirstToken.Sub(sample.SendTime).Seconds())
	}
	if cttfl := sample.CTTFL() / 1000; cttfl > 0 {
		m.hostCTTFLSecondsPerToken.WithLabelValues(labels...).Observe(cttfl)
	}
	if sample.TotalTime > 0 {
		m.hostTotalSeconds.WithLabelValues(labels...).Observe(sample.TotalTime.Seconds())
	}
}

type gatewayMetricsCollector struct {
	gateway         *Gateway
	hostConnections hostConnectionSnapshotter

	inflightRequestsDesc          *prometheus.Desc
	inflightTokensDesc            *prometheus.Desc
	effectiveMaxConcurrentDesc    *prometheus.Desc
	effectiveMaxInputTokensDesc   *prometheus.Desc
	capacityScaleDesc             *prometheus.Desc
	capacityTotalDesc             *prometheus.Desc
	capacityBaselineDesc          *prometheus.Desc
	escrowWeightDesc              *prometheus.Desc
	runtimeActiveDesc             *prometheus.Desc
	runtimeRequestsDesc           *prometheus.Desc
	runtimeReservedDesc           *prometheus.Desc
	participantExhaustedDesc      *prometheus.Desc
	participantTrackedDesc        *prometheus.Desc
	escrowParticipantLimitedDesc  *prometheus.Desc
	escrowBlockedParticipantsDesc *prometheus.Desc
	hostOpenDesc                  *prometheus.Desc
	hostStateDesc                 *prometheus.Desc
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
	ch <- c.escrowWeightDesc
	ch <- c.runtimeActiveDesc
	ch <- c.runtimeRequestsDesc
	ch <- c.runtimeReservedDesc
	ch <- c.participantExhaustedDesc
	ch <- c.participantTrackedDesc
	ch <- c.escrowParticipantLimitedDesc
	ch <- c.escrowBlockedParticipantsDesc
	ch <- c.hostOpenDesc
	ch <- c.hostStateDesc
}

func (c *gatewayMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	if c.gateway == nil {
		return
	}

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

	c.gateway.mu.Lock()
	runtimes := append([]*devshardRuntime(nil), c.gateway.runtimeOrder...)
	c.gateway.mu.Unlock()
	for _, rt := range runtimes {
		active := 0.0
		if rt.active.Load() {
			active = 1
		}
		labels := []string{rt.id, rt.model}
		ch <- prometheus.MustNewConstMetric(c.runtimeActiveDesc, prometheus.GaugeValue, active, labels...)
		ch <- prometheus.MustNewConstMetric(c.runtimeRequestsDesc, prometheus.GaugeValue, float64(rt.activeRequests.Load()), labels...)
		ch <- prometheus.MustNewConstMetric(c.runtimeReservedDesc, prometheus.GaugeValue, float64(rt.reservedTokens.Load()), labels...)
		blocked := 0
		if c.gateway.participantLimiter != nil {
			blocked = len(c.gateway.participantLimiter.BlockedParticipants(rt.participantKeys))
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
