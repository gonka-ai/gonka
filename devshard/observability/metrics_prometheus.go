package observability

import (
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
)

// promInstruments mirrors decentralized-api's Prometheus surface using
// devshardd-specific metric names so dashboards can target one source per
// service.
type promInstruments struct {
	activeOperations  *prometheus.GaugeVec
	operationDuration *prometheus.HistogramVec
	operationErrors   *prometheus.CounterVec
}

var (
	promOnce       sync.Once
	promInstrument promInstruments
	promLabelKeys  = []string{"operation", "model"}
)

// MetricsHandler returns the /metrics handler. Registers instruments lazily so
// it is safe to call before Init.
func MetricsHandler() http.Handler {
	initPromMetrics()
	return promhttp.HandlerFor(Registry(), promhttp.HandlerOpts{Registry: Registry()})
}

func initPromMetrics() {
	promOnce.Do(func() {
		promInstrument = promInstruments{
			activeOperations: prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Name: "devshardd_request_active_operations",
				Help: "In-flight devshardd operations.",
			}, promLabelKeys),
			operationDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Name:    "devshardd_request_duration_seconds",
				Help:    "Duration of devshardd operations.",
				Buckets: prometheus.DefBuckets,
			}, promLabelKeys),
			operationErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "devshardd_request_errors_total",
				Help: "devshardd operations that ended with an error.",
			}, promLabelKeys),
		}
		Registry().MustRegister(
			promInstrument.activeOperations,
			promInstrument.operationDuration,
			promInstrument.operationErrors,
		)
	})
}

func recordOperationStarted(attrs []attribute.KeyValue) {
	initPromMetrics()
	op, model := promLabels(attrs)
	promInstrument.activeOperations.WithLabelValues(op, model).Inc()
}

func recordOperationFinished(attrs []attribute.KeyValue, startedAt time.Time, err error) {
	initPromMetrics()
	op, model := promLabels(attrs)
	if err != nil {
		promInstrument.operationErrors.WithLabelValues(op, model).Inc()
	}
	promInstrument.operationDuration.WithLabelValues(op, model).Observe(time.Since(startedAt).Seconds())
	promInstrument.activeOperations.WithLabelValues(op, model).Dec()
}

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
