package observability

import (
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	registryOnce sync.Once
	registry     *prometheus.Registry

	inflight               *prometheus.GaugeVec
	requestTerminalTotal   *prometheus.CounterVec
	interruptionTotal      *prometheus.CounterVec
	sessionResolutionTotal *prometheus.CounterVec
	receiptOrphanTotal     *prometheus.CounterVec
	validationTotal        *prometheus.CounterVec
	validationOrphanTotal  *prometheus.CounterVec
	validationQueueDrops   prometheus.Counter
	payloadRequestTotal    *prometheus.CounterVec
	mlnodeAttemptsTotal    *prometheus.CounterVec
	mlnodeCallSeconds      *prometheus.HistogramVec
	mlnodeTokens           *prometheus.HistogramVec
	httpConnections        *prometheus.GaugeVec
	httpConnectionsTotal   *prometheus.CounterVec
	validationQueueDepth   *prometheus.GaugeVec
	mempoolSize            *prometheus.GaugeVec
	buildInfo              *prometheus.GaugeVec
)

var durationBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

func Registry() *prometheus.Registry {
	registryOnce.Do(initRegistry)
	return registry
}

func ensureMetrics() {
	Registry()
}

func Handler() http.Handler {
	return promhttp.HandlerFor(Registry(), promhttp.HandlerOpts{})
}

func initRegistry() {
	registry = prometheus.NewRegistry()

	inflight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "devshard_inflight",
		Help: "In-flight devshard operations by stage.",
	}, []string{"stage"})
	requestTerminalTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "devshard_request_terminal_total",
		Help: "Terminal outcome for devshard inference requests.",
	}, []string{"terminal", "reason"})
	interruptionTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "devshard_interruption_total",
		Help: "Operator-actionable devshard interruptions.",
	}, []string{"class", "reason"})
	sessionResolutionTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "devshard_session_resolution_total",
		Help: "Lazy devshard session resolution outcomes.",
	}, []string{"route", "status", "reason"})
	receiptOrphanTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "devshard_receipt_orphan_total",
		Help: "Requests that produced a receipt but did not publish finish.",
	}, []string{"reason"})

	validationTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "devshard_validation_total",
		Help: "Devshard validation lifecycle events.",
	}, []string{"stage", "status"})
	validationOrphanTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "devshard_validation_orphan_total",
		Help: "Validation jobs that did not publish expected validation txs.",
	}, []string{"reason"})
	validationQueueDrops = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "devshard_validation_queue_drops_total",
		Help: "Validation jobs dropped because the queue was full.",
	})
	payloadRequestTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "devshard_payload_request_total",
		Help: "Executor payload-serving request outcomes.",
	}, []string{"status", "reason"})

	mlnodeAttemptsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "devshard_mlnode_attempts_total",
		Help: "ML node attempts for devshard execute and validate paths.",
	}, []string{"path", "outcome", "node_id"})
	mlnodeCallSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "devshard_mlnode_call_seconds",
		Help:    "ML node call latency for devshard paths.",
		Buckets: durationBuckets,
	}, []string{"path", "node_id", "phase"})
	mlnodeTokens = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "devshard_mlnode_tokens",
		Help:    "Token distributions for devshard ML node calls.",
		Buckets: []float64{1, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768},
	}, []string{"path", "node_id", "kind"})

	httpConnections = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "devshard_http_connections",
		Help: "Current HTTP connections for devshard-serving HTTP servers.",
	}, []string{"server", "state"})
	httpConnectionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "devshard_http_connections_total",
		Help: "HTTP connection state transitions for devshard-serving HTTP servers.",
	}, []string{"server", "state"})
	validationQueueDepth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "devshard_validation_queue_depth",
		Help: "Current validation queue depth per devshard session.",
	}, []string{"escrow_id"})
	mempoolSize = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "devshard_mempool_size",
		Help: "Current devshard mempool size per session.",
	}, []string{"escrow_id"})
	buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "devshard_build_info",
		Help: "Devshard build and runtime information.",
	}, []string{"binary", "version", "commit"})

	registry.MustRegister(
		inflight,
		requestTerminalTotal,
		interruptionTotal,
		sessionResolutionTotal,
		receiptOrphanTotal,
		validationTotal,
		validationOrphanTotal,
		validationQueueDrops,
		payloadRequestTotal,
		mlnodeAttemptsTotal,
		mlnodeCallSeconds,
		mlnodeTokens,
		httpConnections,
		httpConnectionsTotal,
		validationQueueDepth,
		mempoolSize,
		buildInfo,
	)
}

// RegisterRuntimeCollectors registers the standard Go runtime / process
// collectors against the devshard private registry. Opt-in: when this
// registry is later merged with another process' default registry (e.g.
// dapi's MergedMetricsHandler), registering Go/process collectors here would
// create duplicate metric families and trigger scrape errors. Call this only
// from the standalone devshardd binary, where this registry is the sole
// source for /metrics.
func RegisterRuntimeCollectors() {
	ensureMetrics()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}

func IncInflight(stage Stage) func() {
	ensureMetrics()
	inflight.WithLabelValues(string(stage)).Inc()
	return func() { inflight.WithLabelValues(string(stage)).Dec() }
}

func IncTerminal(terminal Terminal, reason Reason) {
	ensureMetrics()
	requestTerminalTotal.WithLabelValues(string(terminal), string(reason)).Inc()
}

func IncInterruption(class, reason Reason) {
	ensureMetrics()
	interruptionTotal.WithLabelValues(string(class), string(reason)).Inc()
}

func IncSessionResolution(route string, metricStatus MetricStatus, reason Reason) {
	ensureMetrics()
	sessionResolutionTotal.WithLabelValues(route, string(metricStatus), string(reason)).Inc()
}

func IncReceiptOrphan(reason Reason) {
	ensureMetrics()
	receiptOrphanTotal.WithLabelValues(string(reason)).Inc()
}

func IncValidation(stage Stage, metricStatus MetricStatus) {
	ensureMetrics()
	validationTotal.WithLabelValues(string(stage), string(metricStatus)).Inc()
}

func IncValidationOrphan(reason Reason) {
	ensureMetrics()
	validationOrphanTotal.WithLabelValues(string(reason)).Inc()
}

func IncValidationQueueDrop() {
	ensureMetrics()
	validationQueueDrops.Inc()
}

func IncPayloadRequest(metricStatus MetricStatus, reason Reason) {
	ensureMetrics()
	payloadRequestTotal.WithLabelValues(string(metricStatus), string(reason)).Inc()
}

func IncMLNodeAttempt(path Path, outcome Reason, nodeID string) {
	ensureMetrics()
	mlnodeAttemptsTotal.WithLabelValues(string(path), string(outcome), nodeID).Inc()
}

// ClassifyMLNodeHTTP maps the (response, transport-error, context-error)
// triple from a single ML-node HTTP attempt to the canonical Reason used by
// the mlnode_attempts counter. ctxErr is consulted only when postErr != nil to
// distinguish a client-cancel/timeout from a generic transport failure.
func ClassifyMLNodeHTTP(resp *http.Response, postErr, ctxErr error) Reason {
	if postErr != nil {
		if ctxErr != nil {
			return ReasonTimeout
		}
		return ReasonTransportErr
	}
	if resp == nil {
		return ReasonTransportErr
	}
	switch {
	case resp.StatusCode >= 500:
		return ReasonHTTP5xx
	case resp.StatusCode >= 400:
		return ReasonHTTP4xx
	default:
		return ReasonOK
	}
}

func ObserveMLNodeCall(path Path, nodeID string, phase MetricPhase, started time.Time) {
	ensureMetrics()
	mlnodeCallSeconds.WithLabelValues(string(path), nodeID, string(phase)).Observe(time.Since(started).Seconds())
}

func ObserveTokens(path Path, nodeID string, kind TokenKind, tokens uint64) {
	ensureMetrics()
	mlnodeTokens.WithLabelValues(string(path), nodeID, string(kind)).Observe(float64(tokens))
}

func SetValidationQueueDepth(escrowID string, depth int) {
	ensureMetrics()
	validationQueueDepth.WithLabelValues(escrowID).Set(float64(depth))
}

func SetMempoolSize(escrowID string, size int) {
	ensureMetrics()
	mempoolSize.WithLabelValues(escrowID).Set(float64(size))
}

func SetBuildInfo(binary, version, commit string) {
	ensureMetrics()
	buildInfo.WithLabelValues(binary, version, commit).Set(1)
}
