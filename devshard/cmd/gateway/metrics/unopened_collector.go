package metrics

import "github.com/prometheus/client_golang/prometheus"

// UnopenedEscrow is an active escrow whose session would not open; Reason is one of the Unopened* classes.
type UnopenedEscrow struct {
	EscrowID string
	Model    string
	Reason   string
}

// UnopenedCollector reports each unopened escrow from the source's current list, so an escrow that opens or is deactivated leaves no series.
type UnopenedCollector struct {
	source func() []UnopenedEscrow

	unopened *prometheus.Desc
}

func NewUnopenedCollector(source func() []UnopenedEscrow) *UnopenedCollector {
	return &UnopenedCollector{
		source:   source,
		unopened: gaugeDesc("devshard_gateway_escrow_unopened", "An active escrow whose session would not open and that the republish retries, with the class of what stopped it. See operations.md, \"Boot\".", "devshard_id", "model", "reason"),
	}
}

func (c *UnopenedCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.unopened
}

func (c *UnopenedCollector) Collect(ch chan<- prometheus.Metric) {
	if c.source == nil {
		return
	}
	for _, escrow := range c.source() {
		gauge(ch, c.unopened, 1, metricLabel(escrow.EscrowID, labelUnknown), metricLabel(escrow.Model, labelUnknown), unopenedReasonLabel(escrow.Reason))
	}
}
