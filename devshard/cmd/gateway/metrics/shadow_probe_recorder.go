package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ShadowProbeRecorder holds what the probes sent to shadow participants observed. See operations.md, "The participants setting".
type ShadowProbeRecorder struct {
	probes       *prometheus.CounterVec
	firstToken   *prometheus.HistogramVec
	duration     *prometheus.HistogramVec
	outputTokens *prometheus.CounterVec
}

func NewShadowProbeRecorder(telemetry *Metrics) *ShadowProbeRecorder {
	recorder := &ShadowProbeRecorder{
		probes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_shadow_probes_total",
			Help: "Probes sent to shadow participants, by outcome.",
		}, []string{"participant", "model", "outcome"}),
		firstToken: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "devshard_gateway_shadow_probe_first_token_seconds",
			Help:    "Time from sending a probe to the first streamed byte of a shadow participant's answer.",
			Buckets: latencyBuckets,
		}, []string{"participant", "model"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "devshard_gateway_shadow_probe_duration_seconds",
			Help:    "Time from sending a probe to the end of a served shadow participant's answer.",
			Buckets: latencyBuckets,
		}, []string{"participant", "model"}),
		outputTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_shadow_probe_output_tokens_total",
			Help: "Output tokens shadow participants reported for their probe answers.",
		}, []string{"participant", "model"}),
	}
	telemetry.Register(recorder.probes, recorder.firstToken, recorder.duration, recorder.outputTokens)
	return recorder
}

// ShadowProbeAnswered records an answered probe: its time to first byte, when one streamed, its total time and its output tokens.
func (r *ShadowProbeRecorder) ShadowProbeAnswered(participant, model, outcome string, streamed bool, firstToken, total time.Duration, outputTokens uint64) {
	participantLabel, modelLabel := metricLabel(participant, labelUnknown), metricLabel(model, labelUnknown)
	r.probes.WithLabelValues(participantLabel, modelLabel, metricLabel(outcome, labelUnknown)).Inc()
	if streamed {
		r.firstToken.WithLabelValues(participantLabel, modelLabel).Observe(firstToken.Seconds())
	}
	r.duration.WithLabelValues(participantLabel, modelLabel).Observe(total.Seconds())
	r.outputTokens.WithLabelValues(participantLabel, modelLabel).Add(float64(outputTokens))
}

// ShadowProbeUnanswered records a probe the shadow participant did not answer.
func (r *ShadowProbeRecorder) ShadowProbeUnanswered(participant, model, outcome string) {
	r.probes.WithLabelValues(metricLabel(participant, labelUnknown), metricLabel(model, labelUnknown), metricLabel(outcome, labelUnknown)).Inc()
}
