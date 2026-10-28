package metrics

import "github.com/prometheus/client_golang/prometheus"

type NonceLedgerCollector struct {
	unknownEscrowFacts func() uint64

	unknownEscrowFactsDesc *prometheus.Desc
}

func NewNonceLedgerCollector(unknownEscrowFacts func() uint64) *NonceLedgerCollector {
	return &NonceLedgerCollector{
		unknownEscrowFacts:     unknownEscrowFacts,
		unknownEscrowFactsDesc: counterDesc("devshard_gateway_nonce_ledger_unknown_escrow_facts_total", "Facts the nonce ledger dropped for an escrow it does not hold: one its sweep has not opened yet, or one its retention pruned."),
	}
}

func (c *NonceLedgerCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.unknownEscrowFactsDesc
}

func (c *NonceLedgerCollector) Collect(ch chan<- prometheus.Metric) {
	if c.unknownEscrowFacts == nil {
		return
	}
	counter(ch, c.unknownEscrowFactsDesc, float64(c.unknownEscrowFacts()))
}
