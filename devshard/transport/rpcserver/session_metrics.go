package rpcserver

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"devshard/observability"
)

var (
	sessionHAMetricsOnce sync.Once
	barrierWait          prometheus.Histogram
	barrierTimeouts      prometheus.Counter
	applyLagSeq          prometheus.Gauge
	notifyReconnects     prometheus.Counter
)

func ensureSessionHAMetrics() {
	sessionHAMetricsOnce.Do(func() {
		barrierWait = prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "devshard_peer_rpc_barrier_wait_seconds",
			Help:    "How long Attach waited for same-version peers to apply the new session seq.",
			Buckets: []float64{0.005, 0.02, 0.05, 0.1, 0.25, 0.5, 1},
		})
		barrierTimeouts = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "devshard_peer_rpc_barrier_timeout_total",
			Help: "Attaches that returned a token before every ready peer had applied the seq.",
		})
		applyLagSeq = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "devshard_peer_rpc_apply_lag_seq",
			Help: "Session seq values this child was behind on its last catch-up.",
		})
		notifyReconnects = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "devshard_peer_rpc_notify_reconnect_total",
			Help: "Times the peer-session LISTEN connection dropped and was reopened.",
		})
		observability.Registry().MustRegister(barrierWait, barrierTimeouts, applyLagSeq, notifyReconnects)
	})
}
