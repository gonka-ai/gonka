package metrics

import "github.com/prometheus/client_golang/prometheus"

// CacheResultRecorder counts what the response cache did with each reply it was offered, stored or refused and why.
type CacheResultRecorder struct {
	results *prometheus.CounterVec
}

func NewCacheResultRecorder(telemetry *Metrics) *CacheResultRecorder {
	recorder := &CacheResultRecorder{
		results: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_cache_results_total",
			Help: "Replies offered to the response cache, by model and by whether it stored them or why it refused.",
		}, []string{"model", "result"}),
	}
	telemetry.Register(recorder.results)
	return recorder
}

func (r *CacheResultRecorder) CacheResult(model, result string) {
	if r == nil {
		return
	}
	r.results.WithLabelValues(metricLabel(model, labelUnknown), metricLabel(result, labelUnknown)).Inc()
}
