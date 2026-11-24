package metrics

import "github.com/prometheus/client_golang/prometheus"

// ServedBindingRecorder counts every served stream the session checked against its host's signed Finish.
type ServedBindingRecorder struct {
	bindings *prometheus.CounterVec
}

func NewServedBindingRecorder(telemetry *Metrics) *ServedBindingRecorder {
	recorder := &ServedBindingRecorder{
		bindings: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_served_bindings_total",
			Help: "Total served streams checked against the Finish their host signed, by verdict.",
		}, []string{"model", "verdict"}),
	}
	telemetry.Register(recorder.bindings)
	return recorder
}

func (r *ServedBindingRecorder) Observed(model, verdict string) {
	if r == nil {
		return
	}
	r.bindings.WithLabelValues(metricLabel(model, labelUnknown), metricLabel(verdict, labelUnknown)).Inc()
}
