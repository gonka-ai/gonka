package devshardobs

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	lookupFanoutErrors atomic.Uint64
	lookupWarnLastUnixNano atomic.Int64

	promOnce sync.Once

	metricLookupErrors prometheus.Counter
	metricFanouts      prometheus.Counter
	metricPrimaryPins  prometheus.Counter
	metricAggregates   prometheus.Counter
	metricLookupHits   prometheus.Counter
)

const lookupWarnInterval = 30 * time.Second

func ensurePromMetrics() {
	promOnce.Do(func() {
		metricLookupErrors = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "devshardobs_lookup_errors_total",
			Help: "Session version PG lookup failures that fell back to fan-out",
		})
		metricFanouts = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "devshardobs_fanouts_total",
			Help: "Versionless session-scoped requests that used fan-out across children",
		})
		metricPrimaryPins = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "devshardobs_primary_pins_total",
			Help: "Process-level obs requests pinned to newest running version (e.g. /metrics)",
		})
		metricAggregates = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "devshardobs_stats_shards_aggregates_total",
			Help: "Versionless /stats/shards list requests served via multi-version merge",
		})
		metricLookupHits = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "devshardobs_lookup_hits_total",
			Help: "Session-scoped requests routed by PG escrow→version lookup",
		})
		prometheus.MustRegister(
			metricLookupErrors,
			metricFanouts,
			metricPrimaryPins,
			metricAggregates,
			metricLookupHits,
		)
	})
}

// LookupFanoutErrors returns how many times versionless obs degraded from PG
// lookup failure to fan-out since process start.
func LookupFanoutErrors() uint64 {
	return lookupFanoutErrors.Load()
}

func noteLookupFanout(escrowID string, err error) {
	ensurePromMetrics()
	n := lookupFanoutErrors.Add(1)
	metricLookupErrors.Inc()
	now := time.Now().UnixNano()
	last := lookupWarnLastUnixNano.Load()
	if now-last < lookupWarnInterval.Nanoseconds() {
		return
	}
	if !lookupWarnLastUnixNano.CompareAndSwap(last, now) {
		return
	}
	slog.Warn("devshardobs: session version lookup failed; falling back to fan-out",
		"escrow_id", escrowID,
		"error", err,
		"fanout_errors_total", n,
	)
}

func noteFanout() {
	ensurePromMetrics()
	metricFanouts.Inc()
}

func notePrimaryPin() {
	ensurePromMetrics()
	metricPrimaryPins.Inc()
}

func noteStatsAggregate() {
	ensurePromMetrics()
	metricAggregates.Inc()
}

func noteLookupHit() {
	ensurePromMetrics()
	metricLookupHits.Inc()
}

func resetLookupFanoutTelemetryForTest() {
	lookupFanoutErrors.Store(0)
	lookupWarnLastUnixNano.Store(0)
}
