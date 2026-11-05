// Package metrics owns the gateway's Prometheus registry; family names are frozen as devshard_*.
// See operations.md, "Metrics".
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// otherMethodLabel absorbs every method outside knownMethods. See README.md, "Cardinality in practice".
const otherMethodLabel = "other"

var knownMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodPost:    true,
	http.MethodPut:     true,
	http.MethodPatch:   true,
	http.MethodDelete:  true,
	http.MethodOptions: true,
}

func methodLabel(method string) string {
	if knownMethods[method] {
		return method
	}
	return otherMethodLabel
}

type Metrics struct {
	registry            *prometheus.Registry
	httpRequestsTotal   *prometheus.CounterVec
	httpRequestDuration *prometheus.HistogramVec
}

func New() *Metrics {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	httpRequestsTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "devshard_http_requests_total",
			Help: "Total HTTP requests handled by the devshard gateway.",
		},
		[]string{"path", "method", "status"},
	)
	httpRequestDuration := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "devshard_http_request_duration_seconds",
			Help:    "HTTP request duration by route.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"path", "method"},
	)
	registry.MustRegister(httpRequestsTotal, httpRequestDuration)

	return &Metrics{
		registry:            registry,
		httpRequestsTotal:   httpRequestsTotal,
		httpRequestDuration: httpRequestDuration,
	}
}

func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Register runs at construction time, where a duplicate family must stop the process, not drop a series.
func (m *Metrics) Register(metricCollectors ...prometheus.Collector) {
	m.registry.MustRegister(metricCollectors...)
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// InstrumentRoute takes the route PATTERN ("/devshard/{id}/v1/status"), never the raw request path.
func (m *Metrics) InstrumentRoute(routeLabel string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		method := methodLabel(r.Method)
		m.httpRequestsTotal.WithLabelValues(routeLabel, method, strconv.Itoa(recorder.status)).Inc()
		m.httpRequestDuration.WithLabelValues(routeLabel, method).Observe(time.Since(startedAt).Seconds())
	})
}

// statusRecorder captures the response status for labeling. See README.md, "How a collector reaches its source".
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
