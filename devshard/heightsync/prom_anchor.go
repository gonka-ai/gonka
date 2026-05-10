package heightsync

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Prometheus metric names (contract with CONTAINER_E2E_PLAN.md §4.2).
const (
	MetricOutboundAnchorsTotal = "devshard_heightsync_outbound_anchors_total"
	MetricInboundAnchorsTotal  = "devshard_heightsync_inbound_anchors_total"
)

var (
	anchorPromMu           sync.RWMutex
	outboundAnchorsCounter *prometheus.CounterVec
	inboundAnchorsCounter  *prometheus.CounterVec
)

// RegisterAnchorMetrics registers height-sync anchor counters on reg.
// Safe to call once per process; subsequent calls are no-ops.
func RegisterAnchorMetrics(reg prometheus.Registerer) error {
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
	if err := reg.Register(outboundAnchorsCounter); err != nil {
		return err
	}
	if err := reg.Register(inboundAnchorsCounter); err != nil {
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
