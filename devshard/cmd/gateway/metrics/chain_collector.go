package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"devshard/cmd/gateway/chain"
)

type PhaseSource interface {
	Snapshot() chain.PhaseSnapshot
}

var (
	epochPhases  = chain.AllEpochPhases()
	blockReasons = chain.AllBlockReasons()
)

type ChainCollector struct {
	phases PhaseSource
	now    func() time.Time

	blockHeight     *prometheus.Desc
	epochIndex      *prometheus.Desc
	epochSwitch     *prometheus.Desc
	maxNonce        *prometheus.Desc
	requestsBlocked *prometheus.Desc
	snapshotAge     *prometheus.Desc
	healthy         *prometheus.Desc
	epochPhase      *prometheus.Desc
	blockReason     *prometheus.Desc
}

func NewChainCollector(phases PhaseSource, now func() time.Time) *ChainCollector {
	return &ChainCollector{
		phases:          phases,
		now:             now,
		blockHeight:     gaugeDesc("devshard_gateway_chain_block_height", "Latest chain block height the observer published."),
		epochIndex:      gaugeDesc("devshard_gateway_chain_epoch_index", "Latest epoch index the observer published."),
		epochSwitch:     gaugeDesc("devshard_gateway_chain_epoch_switch_block_height", "Block height at which the current epoch switches."),
		maxNonce:        gaugeDesc("devshard_gateway_chain_max_nonce", "Escrow max-nonce parameter; 0 means not yet fetched."),
		requestsBlocked: gaugeDesc("devshard_gateway_chain_requests_blocked", "Whether the raw chain phase blocks new requests."),
		snapshotAge:     gaugeDesc("devshard_gateway_chain_snapshot_age_seconds", "Age of the published chain snapshot."),
		healthy:         gaugeDesc("devshard_gateway_chain_snapshot_healthy", "Whether the last observer refresh succeeded."),
		epochPhase:      gaugeDesc("devshard_gateway_chain_epoch_phase", "Current epoch phase (1 for the active one).", "phase"),
		blockReason:     gaugeDesc("devshard_gateway_chain_block_reason", "Current request-block reason (1 for the active one).", "reason"),
	}
}

func (c *ChainCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{
		c.blockHeight, c.epochIndex, c.epochSwitch, c.maxNonce, c.requestsBlocked,
		c.snapshotAge, c.healthy, c.epochPhase, c.blockReason,
	} {
		ch <- desc
	}
}

func (c *ChainCollector) Collect(ch chan<- prometheus.Metric) {
	snapshot := c.phases.Snapshot()
	gauge(ch, c.blockHeight, float64(snapshot.BlockHeight))
	gauge(ch, c.epochIndex, float64(snapshot.EpochIndex))
	gauge(ch, c.epochSwitch, float64(snapshot.EpochSwitchBlockHeight))
	gauge(ch, c.maxNonce, float64(snapshot.MaxNonce))
	gauge(ch, c.requestsBlocked, boolGauge(snapshot.RequestsBlocked))
	gauge(ch, c.snapshotAge, elapsedSeconds(snapshot.LastUpdatedAt, c.now()))
	gauge(ch, c.healthy, boolGauge(snapshot.LastError == ""))
	for _, phase := range epochPhases {
		gauge(ch, c.epochPhase, boolGauge(snapshot.EpochPhase == phase), string(phase))
	}
	for _, reason := range blockReasons {
		gauge(ch, c.blockReason, boolGauge(snapshot.BlockReason == reason), metricLabel(string(reason), reasonNone))
	}
}
