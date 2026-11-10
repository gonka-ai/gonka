package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"devshard/cmd/gateway/store"
)

// OwedVotesSource is the owed refusal votes writer's own count of what it lost; *store.OwedVotes satisfies it.
type OwedVotesSource interface {
	Stats() store.OwedVoteStats
}

type OwedVotesCollector struct {
	owedVotes OwedVotesSource

	lost *prometheus.Desc
}

func NewOwedVotesCollector(owedVotes OwedVotesSource) *OwedVotesCollector {
	return &OwedVotesCollector{
		owedVotes: owedVotes,
		lost:      counterDesc("devshard_gateway_owed_vote_writes_lost_total", "Owed refusal vote saves and deletes the store lost, by cause; a lost save leaves the vote in memory only.", "cause"),
	}
}

func (c *OwedVotesCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.lost
}

func (c *OwedVotesCollector) Collect(ch chan<- prometheus.Metric) {
	if c.owedVotes == nil {
		return
	}
	stats := c.owedVotes.Stats()
	counter(ch, c.lost, float64(stats.Dropped), lostCauseShed)
	counter(ch, c.lost, float64(stats.Failed), lostCauseWriteFailed)
}
