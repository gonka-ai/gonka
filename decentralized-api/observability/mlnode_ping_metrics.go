package observability

import (
	"strings"
	"sync"
	"time"

	"common/probe"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	mlnodePingOnce         sync.Once
	mlnodePingUp           *prometheus.GaugeVec
	mlnodePingRTT          *prometheus.GaugeVec
	mlnodePingWarmRTT      prometheus.Histogram
	mlnodePingDivergence   *prometheus.GaugeVec
	mlnodePingLastProbe    *prometheus.GaugeVec
	mlnodePingProbeKind    *prometheus.GaugeVec
	mlnodePingTargets      prometheus.Gauge
	mlnodePingTicks        prometheus.Counter
	mlnodePingTicksSkipped prometheus.Counter
)

func initMLNodePingMetrics() {
	mlnodePingOnce.Do(func() {
		mlnodePingUp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dapi_mlnode_ping_up",
			Help: "Whether the last ML node ping probe succeeded (1) or failed (0).",
		}, []string{"node_id"})
		mlnodePingRTT = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dapi_mlnode_ping_rtt_seconds",
			Help: "Last warm ML node ping RTT in seconds.",
		}, []string{"node_id"})
		mlnodePingWarmRTT = prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "dapi_mlnode_ping_warm_rtt_seconds",
			Help:    "Fleet-wide warm ML node ping RTT distribution (no node_id label).",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14),
		})
		mlnodePingDivergence = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dapi_mlnode_clock_divergence_seconds",
			Help: "Last estimated clock divergence versus an ML node; omitted when no timestamp is available.",
		}, []string{"node_id", "source"})
		mlnodePingLastProbe = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dapi_mlnode_ping_last_probe_timestamp_seconds",
			Help: "Unix timestamp of the last ML node ping probe attempt.",
		}, []string{"node_id"})
		mlnodePingProbeKind = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dapi_mlnode_ping_probe_kind",
			Help: "Sticky probe capability kind for an ML node (info-style gauge).",
		}, []string{"node_id", "kind"})
		mlnodePingTargets = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dapi_mlnode_ping_targets",
			Help: "Number of ML nodes currently probed by the dapi ping job.",
		})
		mlnodePingTicks = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "dapi_mlnode_ping_ticks_total",
			Help: "Total ML node ping scheduler ticks started.",
		})
		mlnodePingTicksSkipped = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "dapi_mlnode_ping_ticks_skipped_total",
			Help: "Total ML node ping scheduler ticks skipped because a prior tick was still in flight.",
		})
		prometheus.MustRegister(
			mlnodePingUp,
			mlnodePingRTT,
			mlnodePingWarmRTT,
			mlnodePingDivergence,
			mlnodePingLastProbe,
			mlnodePingProbeKind,
			mlnodePingTargets,
			mlnodePingTicks,
			mlnodePingTicksSkipped,
		)
	})
}

// ObserveMLNodePingResult publishes one probe sample onto the default registry.
func ObserveMLNodePingResult(r probe.Result) {
	initMLNodePingMetrics()
	nodeID := strings.TrimSpace(r.Key)
	if nodeID == "" {
		return
	}
	if r.Up {
		mlnodePingUp.WithLabelValues(nodeID).Set(1)
	} else {
		mlnodePingUp.WithLabelValues(nodeID).Set(0)
	}
	at := r.At
	if at.IsZero() {
		at = time.Now()
	}
	mlnodePingLastProbe.WithLabelValues(nodeID).Set(float64(at.Unix()))

	kind := r.Kind.String()
	for _, k := range []string{probe.KindClock.String(), probe.KindDate.String(), probe.KindHealth.String(), probe.KindNone.String()} {
		v := 0.0
		if k == kind {
			v = 1
		}
		mlnodePingProbeKind.WithLabelValues(nodeID, k).Set(v)
	}

	if r.Up && r.ConnReused && r.RTT > 0 {
		sec := r.RTT.Seconds()
		mlnodePingRTT.WithLabelValues(nodeID).Set(sec)
		mlnodePingWarmRTT.Observe(sec)
	}

	if r.HasDivergence {
		src := r.DivergenceSource.String()
		if src == "" || src == probe.KindNone.String() {
			src = kind
		}
		mlnodePingDivergence.WithLabelValues(nodeID, src).Set(r.Divergence.Seconds())
	} else {
		mlnodePingDivergence.DeleteLabelValues(nodeID, probe.KindClock.String())
		mlnodePingDivergence.DeleteLabelValues(nodeID, probe.KindDate.String())
	}
}

// DeleteMLNodePingMetrics drops per-node series when a node leaves inventory.
func DeleteMLNodePingMetrics(nodeID string) {
	initMLNodePingMetrics()
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return
	}
	mlnodePingUp.DeleteLabelValues(nodeID)
	mlnodePingRTT.DeleteLabelValues(nodeID)
	mlnodePingLastProbe.DeleteLabelValues(nodeID)
	for _, src := range []string{probe.KindClock.String(), probe.KindDate.String()} {
		mlnodePingDivergence.DeleteLabelValues(nodeID, src)
	}
	for _, k := range []string{probe.KindClock.String(), probe.KindDate.String(), probe.KindHealth.String(), probe.KindNone.String()} {
		mlnodePingProbeKind.DeleteLabelValues(nodeID, k)
	}
}

func SetMLNodePingTargets(n int) {
	initMLNodePingMetrics()
	mlnodePingTargets.Set(float64(n))
}

func IncMLNodePingTicks() {
	initMLNodePingMetrics()
	mlnodePingTicks.Inc()
}

func IncMLNodePingTicksSkipped() {
	initMLNodePingMetrics()
	mlnodePingTicksSkipped.Inc()
}
