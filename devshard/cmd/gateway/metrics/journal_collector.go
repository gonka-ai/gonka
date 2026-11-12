package metrics

import "github.com/prometheus/client_golang/prometheus"

// JournalCounts is what the journal refused, dropped and received too late; plain integers keep metrics free of an edge to journal.
type JournalCounts struct {
	MoneyRefused    uint64
	ProgressDropped uint64
	LateEvents      uint64
}

type JournalCollector struct {
	counts func() JournalCounts

	moneyRefused    *prometheus.Desc
	progressDropped *prometheus.Desc
	lateEvents      *prometheus.Desc
}

func NewJournalCollector(counts func() JournalCounts) *JournalCollector {
	return &JournalCollector{
		counts:          counts,
		moneyRefused:    counterDesc("devshard_gateway_journal_money_refused_total", "Money-lane events the journal refused past its ceiling: a nonce, burn or vote the nonce ledger never applied, or a money-path line never written."),
		progressDropped: counterDesc("devshard_gateway_journal_progress_dropped_total", "Progress lines the journal dropped while its consumer was behind."),
		lateEvents:      counterDesc("devshard_gateway_journal_late_events_total", "Events that reached the journal after it closed, from work still running when the journal closes."),
	}
}

func (c *JournalCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.moneyRefused
	ch <- c.progressDropped
	ch <- c.lateEvents
}

func (c *JournalCollector) Collect(ch chan<- prometheus.Metric) {
	if c.counts == nil {
		return
	}
	current := c.counts()
	counter(ch, c.moneyRefused, float64(current.MoneyRefused))
	counter(ch, c.progressDropped, float64(current.ProgressDropped))
	counter(ch, c.lateEvents, float64(current.LateEvents))
}
