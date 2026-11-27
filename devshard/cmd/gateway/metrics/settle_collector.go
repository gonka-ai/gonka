package metrics

import "github.com/prometheus/client_golang/prometheus"

// SettleCollector reports the nonces forced settles finalized past while they were unresolved in the completion journal.
type SettleCollector struct {
	unresolved func() uint64

	settledUnresolved *prometheus.Desc
}

// NewSettleCollector reads the unresolved-nonce count on every scrape.
func NewSettleCollector(unresolved func() uint64) *SettleCollector {
	return &SettleCollector{
		unresolved:        unresolved,
		settledUnresolved: counterDesc("devshard_gateway_escrow_settled_unresolved_nonces_total", "Started nonces a forced settle finalized past while they were still unresolved in the completion journal."),
	}
}

func (collector *SettleCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- collector.settledUnresolved
}

func (collector *SettleCollector) Collect(ch chan<- prometheus.Metric) {
	if collector.unresolved == nil {
		return
	}
	counter(ch, collector.settledUnresolved, float64(collector.unresolved()))
}
