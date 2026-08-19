package heightsync

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// Prometheus metric names (contract with CONTAINER_E2E_PLAN.md §4.2).
const (
	MetricOutboundAnchorsTotal = "devshard_heightsync_outbound_anchors_total"
	MetricInboundAnchorsTotal  = "devshard_heightsync_inbound_anchors_total"
	// MetricOracleFailuresTotal counts times an Anchor was due but the block oracle
	// returned an error or nil header (non-force paths degrade to Omit).
	MetricOracleFailuresTotal   = "devshard_heightsync_oracle_failures_total"
	MetricLazyAnchorsTotal      = "devshard_heightsync_lazy_anchor_total"
	MetricStaleOriginRejected   = "devshard_heightsync_stale_origin_rejected_total"
	MetricOriginSigInvalidTotal = "devshard_heightsync_origin_sig_invalid_total"
)

var (
	anchorPromMu            sync.RWMutex
	outboundAnchorsCounter  *prometheus.CounterVec
	inboundAnchorsCounter   *prometheus.CounterVec
	oracleFailuresCounter   *prometheus.CounterVec
	lazyAnchorsCounter      prometheus.Counter
	staleOriginCounter      prometheus.Counter
	originSigInvalidCounter prometheus.Counter
)

// RegisterAnchorMetrics registers height-sync anchor counters on reg.
// Safe to call once per process; subsequent calls are no-ops.
func RegisterAnchorMetrics(reg prometheus.Registerer) error {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	anchorPromMu.Lock()
	defer anchorPromMu.Unlock()
	if outboundAnchorsCounter != nil {
		return nil
	}
	outboundAnchorsCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: MetricOutboundAnchorsTotal,
			Help: "Height-sync Anchor sections emitted (request=user outbound, response=host outbound).",
		},
		[]string{"direction", "escrow_id", "host_id"},
	)
	inboundAnchorsCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: MetricInboundAnchorsTotal,
			Help: "Height-sync Anchor sections observed inbound (request=user->host, response=host->user SSE).",
		},
		[]string{"direction", "trust_level", "escrow_id"},
	)
	oracleFailuresCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: MetricOracleFailuresTotal,
			Help: "Block oracle failures while an Anchor emission was required (cadence/forced/session-start).",
		},
		[]string{"host_id"},
	)
	lazyAnchorsCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: MetricLazyAnchorsTotal,
		Help: "Inbound user Anchors classified VALID_LAZY_ANCHOR (omit-window carry-forward).",
	})
	staleOriginCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: MetricStaleOriginRejected,
		Help: "Inbound carry-forward Anchors rejected for stale originator timestamp.",
	})
	originSigInvalidCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: MetricOriginSigInvalidTotal,
		Help: "Response-leg height-sync Anchors dropped due to invalid or missing originator signature.",
	})
	if err := reg.Register(outboundAnchorsCounter); err != nil {
		return err
	}
	if err := reg.Register(inboundAnchorsCounter); err != nil {
		return err
	}
	if err := reg.Register(oracleFailuresCounter); err != nil {
		return err
	}
	if err := reg.Register(lazyAnchorsCounter); err != nil {
		return err
	}
	if err := reg.Register(staleOriginCounter); err != nil {
		return err
	}
	if err := reg.Register(originSigInvalidCounter); err != nil {
		return err
	}
	return nil
}

// IncOutboundAnchor increments outbound anchor counter when metrics are registered.
func IncOutboundAnchor(direction, escrowID, hostID string) {
	anchorPromMu.RLock()
	c := outboundAnchorsCounter
	anchorPromMu.RUnlock()
	if c == nil {
		return
	}
	c.WithLabelValues(direction, escrowID, hostID).Inc()
}

// IncInboundAnchor increments inbound anchor counter when metrics are registered.
func IncInboundAnchor(direction, trustLevel, escrowID string) {
	anchorPromMu.RLock()
	c := inboundAnchorsCounter
	anchorPromMu.RUnlock()
	if c == nil {
		return
	}
	if trustLevel == "" {
		trustLevel = "unset"
	}
	c.WithLabelValues(direction, trustLevel, escrowID).Inc()
}

// IncOracleFailure increments the oracle-failure counter when metrics are registered.
// hostID should be the local validator address (user or host); use "unknown" if empty.
func IncOracleFailure(hostID string) {
	anchorPromMu.RLock()
	c := oracleFailuresCounter
	anchorPromMu.RUnlock()
	if c == nil {
		return
	}
	if hostID == "" {
		hostID = "unknown"
	}
	c.WithLabelValues(hostID).Inc()
}

// IncLazyAnchor increments lazy-anchor counter when metrics are registered.
func IncLazyAnchor() {
	anchorPromMu.RLock()
	c := lazyAnchorsCounter
	anchorPromMu.RUnlock()
	if c != nil {
		c.Inc()
	}
}

// IncStaleOriginRejected increments stale-origin rejection counter when registered.
func IncStaleOriginRejected() {
	anchorPromMu.RLock()
	c := staleOriginCounter
	anchorPromMu.RUnlock()
	if c != nil {
		c.Inc()
	}
}

func counterSnapshot(c prometheus.Counter) float64 {
	if c == nil {
		return 0
	}
	var m dto.Metric
	if err := c.(prometheus.Metric).Write(&m); err != nil {
		return 0
	}
	return m.GetCounter().GetValue()
}

// StaleOriginRejectedTotal returns the stale-origin counter (0 if metrics not registered).
func StaleOriginRejectedTotal() float64 {
	anchorPromMu.RLock()
	c := staleOriginCounter
	anchorPromMu.RUnlock()
	return counterSnapshot(c)
}

// LazyAnchorTotal returns the lazy-anchor counter (0 if metrics not registered).
func LazyAnchorTotal() float64 {
	anchorPromMu.RLock()
	c := lazyAnchorsCounter
	anchorPromMu.RUnlock()
	return counterSnapshot(c)
}

// IncOriginSigInvalid increments invalid/missing response origin signature counter.
func IncOriginSigInvalid() {
	anchorPromMu.RLock()
	c := originSigInvalidCounter
	anchorPromMu.RUnlock()
	if c != nil {
		c.Inc()
	}
}

// OriginSigInvalidTotal returns the origin-sig-invalid counter (0 if not registered).
func OriginSigInvalidTotal() float64 {
	anchorPromMu.RLock()
	c := originSigInvalidCounter
	anchorPromMu.RUnlock()
	return counterSnapshot(c)
}
