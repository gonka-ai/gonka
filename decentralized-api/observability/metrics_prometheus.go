package observability

import (
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
)

// promInstruments mirrors the OTel instruments into Prometheus so the local
// /metrics endpoint stays self-contained — operators can scrape directly even
// when an OTLP collector is unreachable.
type promInstruments struct {
	activeOperations  *prometheus.GaugeVec
	operationDuration *prometheus.HistogramVec
	operationErrors   *prometheus.CounterVec
	promptTokens      *prometheus.HistogramVec
	completionTokens  *prometheus.HistogramVec
	totalTokens       *prometheus.HistogramVec
}

var (
	promOnce       sync.Once
	promInstrument promInstruments
	promLabelKeys  = []string{"operation", "model"}
)

// MetricsHandler exposes Prometheus metrics. Safe to call before Init: the
// instruments are lazily registered against the default Prometheus registry on
// first use.
func MetricsHandler() http.Handler {
	initPrometheusMetrics()
	return promhttp.Handler()
}

// MergedMetricsHandler exposes the default Prometheus registry merged with any
// additional registries passed in. Used by api to surface both its own
// decentralized_api_* metrics (default registry) and the devshard package's
// devshard_* metrics (private registry) on a single /metrics endpoint.
//
// HandlerOpts.ErrorHandling is set to ContinueOnError so a single misbehaving
// collector (e.g. one that produces duplicate label tuples on a transient
// snapshot) cannot blank-out the entire scrape: working metrics are still
// served, and the error is logged on Prometheus's side and surfaced via
// promhttp_metric_handler_errors_total.
func MergedMetricsHandler(extra ...prometheus.Gatherer) http.Handler {
	initPrometheusMetrics()
	gatherers := make(prometheus.Gatherers, 0, len(extra)+1)
	gatherers = append(gatherers, prometheus.DefaultGatherer)
	gatherers = append(gatherers, extra...)
	return promhttp.HandlerFor(gatherers, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
	})
}

func initPrometheusMetrics() {
	promOnce.Do(func() {
		promInstrument = promInstruments{
			activeOperations: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Name: "decentralized_api_inference_active_operations",
				Help: "Number of in-flight decentralized-api inference operations.",
			}, promLabelKeys),
			operationDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Name:    "decentralized_api_inference_operation_duration_seconds",
				Help:    "Duration of decentralized-api inference operations.",
				Buckets: prometheus.DefBuckets,
			}, promLabelKeys),
			operationErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "decentralized_api_inference_operation_errors_total",
				Help: "Decentralized-api inference operations that ended with an error.",
			}, promLabelKeys),
			promptTokens: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Name:    "decentralized_api_inference_prompt_tokens",
				Help:    "Prompt token counts recorded by inference operations.",
				Buckets: prometheus.ExponentialBuckets(16, 2, 14),
			}, promLabelKeys),
			completionTokens: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Name:    "decentralized_api_inference_completion_tokens",
				Help:    "Completion token counts recorded by inference operations.",
				Buckets: prometheus.ExponentialBuckets(16, 2, 14),
			}, promLabelKeys),
			totalTokens: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Name:    "decentralized_api_inference_total_tokens",
				Help:    "Total tokens (prompt + completion) recorded by inference operations.",
				Buckets: prometheus.ExponentialBuckets(16, 2, 14),
			}, promLabelKeys),
		}
		prometheus.MustRegister(
			promInstrument.activeOperations,
			promInstrument.operationDuration,
			promInstrument.operationErrors,
			promInstrument.promptTokens,
			promInstrument.completionTokens,
			promInstrument.totalTokens,
		)
	})
}

func recordPrometheusOperationStarted(attrs []attribute.KeyValue) {
	initPrometheusMetrics()
	op, model := promLabels(attrs)
	promInstrument.activeOperations.WithLabelValues(op, model).Inc()
}

func recordPrometheusOperationTokens(attrs []attribute.KeyValue, prompt, completion uint64) {
	initPrometheusMetrics()
	op, model := promLabels(attrs)
	if prompt > 0 {
		promInstrument.promptTokens.WithLabelValues(op, model).Observe(float64(prompt))
	}
	if completion > 0 {
		promInstrument.completionTokens.WithLabelValues(op, model).Observe(float64(completion))
	}
	if total := prompt + completion; total > 0 {
		promInstrument.totalTokens.WithLabelValues(op, model).Observe(float64(total))
	}
}

func recordPrometheusOperationFinished(attrs []attribute.KeyValue, startedAt time.Time, err error) {
	initPrometheusMetrics()
	op, model := promLabels(attrs)
	if err != nil {
		promInstrument.operationErrors.WithLabelValues(op, model).Inc()
	}
	promInstrument.operationDuration.WithLabelValues(op, model).Observe(time.Since(startedAt).Seconds())
	promInstrument.activeOperations.WithLabelValues(op, model).Dec()
}

// promLabels picks the two labels exposed in Prometheus from the OTel
// attribute set. Anything outside the allowlist is dropped to keep cardinality
// bounded — high-cardinality dimensions belong on traces, not on Prom series.
func promLabels(attrs []attribute.KeyValue) (operation, model string) {
	operation, model = "unknown", "unknown"
	for _, a := range attrs {
		switch string(a.Key) {
		case "operation":
			if v := a.Value.AsString(); v != "" {
				operation = v
			}
		case "model":
			if v := a.Value.AsString(); v != "" {
				model = v
			}
		}
	}
	return operation, model
}
