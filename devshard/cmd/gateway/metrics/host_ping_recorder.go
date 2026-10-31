package metrics

import (
	"common/probe"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	hostPingBucketStartSeconds = 0.001
	hostPingBucketCount        = 12
)

// HostPingRecorder holds what the host pings observed. See ../hostping/README.md.
type HostPingRecorder struct {
	up         *prometheus.GaugeVec
	rtt        *prometheus.GaugeVec
	warmRTT    prometheus.Histogram
	divergence *prometheus.GaugeVec
	lastProbe  *prometheus.GaugeVec
	targets    prometheus.Gauge
	ticks      *prometheus.CounterVec
}

func NewHostPingRecorder(telemetry *Metrics) *HostPingRecorder {
	recorder := &HostPingRecorder{
		up: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "devshard_gateway_host_ping_up",
			Help: "Whether the last ping of this host was answered.",
		}, []string{"participant_key"}),
		rtt: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "devshard_gateway_host_ping_rtt_seconds",
			Help: "Round trip of the last ping of this host, the host's own processing subtracted.",
		}, []string{"participant_key"}),
		warmRTT: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "devshard_gateway_host_ping_warm_rtt_seconds",
			Help:    "Round trips over a reused connection, which is the fleet's distribution without dial cost.",
			Buckets: prometheus.ExponentialBuckets(hostPingBucketStartSeconds, bucketGrowthFactor, hostPingBucketCount),
		}),
		divergence: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "devshard_gateway_host_clock_divergence_seconds",
			Help: "How far this host's clock sits from the gateway's, by where the reading came from.",
		}, []string{"participant_key", "source"}),
		lastProbe: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "devshard_gateway_host_ping_last_probe_timestamp_seconds",
			Help: "When this host was last pinged, so a stale series is visible as stale.",
		}, []string{"participant_key"}),
		targets: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "devshard_gateway_host_ping_targets",
			Help: "Hosts the live escrows currently give the prober to reach.",
		}),
		ticks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_host_ping_ticks_total",
			Help: "Probe waves the scheduler started, and the ones it skipped because the last had not finished.",
		}, []string{"outcome"}),
	}
	telemetry.Register(recorder.up, recorder.rtt, recorder.warmRTT, recorder.divergence,
		recorder.lastProbe, recorder.targets, recorder.ticks)
	return recorder
}

func (r *HostPingRecorder) Observe(result probe.Result) {
	if r == nil {
		return
	}
	participant := metricLabel(result.Key, labelUnknown)
	r.up.WithLabelValues(participant).Set(boolGauge(result.Up))
	if !result.At.IsZero() {
		r.lastProbe.WithLabelValues(participant).Set(float64(result.At.Unix()))
	}
	if !result.Up {
		return
	}
	seconds := result.RTT.Seconds()
	r.rtt.WithLabelValues(participant).Set(seconds)
	if result.ConnReused {
		r.warmRTT.Observe(seconds)
	}
	if result.HasDivergence {
		r.divergence.WithLabelValues(participant, result.DivergenceSource.String()).Set(result.Divergence.Seconds())
	}
}

func (r *HostPingRecorder) Forget(key string) {
	if r == nil {
		return
	}
	participant := metricLabel(key, labelUnknown)
	r.up.DeleteLabelValues(participant)
	r.rtt.DeleteLabelValues(participant)
	r.lastProbe.DeleteLabelValues(participant)
	r.divergence.DeletePartialMatch(prometheus.Labels{"participant_key": participant})
}

func (r *HostPingRecorder) TickStarted() { r.countTick(HostPingTickStarted) }

func (r *HostPingRecorder) TickSkipped() { r.countTick(HostPingTickSkipped) }

func (r *HostPingRecorder) TargetCount(count int) {
	if r != nil {
		r.targets.Set(float64(count))
	}
}

func (r *HostPingRecorder) countTick(outcome string) {
	if r != nil {
		r.ticks.WithLabelValues(outcome).Inc()
	}
}
