package metrics

import "github.com/prometheus/client_golang/prometheus"

// LimitRecorder is the only place the limiter's rejections are counted; the caps are LimitsCollector gauges.
type LimitRecorder struct {
	rejections *prometheus.CounterVec
}

func NewLimitRecorder(telemetry *Metrics) *LimitRecorder {
	recorder := &LimitRecorder{
		rejections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_limit_rejections_total",
			Help: "Total requests the gateway limiter turned away, by which cap they ran into.",
		}, []string{"model", "reason"}),
	}
	telemetry.Register(recorder.rejections)
	return recorder
}

func (r *LimitRecorder) Rejected(model, reason string) {
	if r == nil {
		return
	}
	r.rejections.WithLabelValues(metricLabel(model, labelUnknown), metricLabel(reason, labelUnknown)).Inc()
}
