package metrics

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"

	"devshard/cmd/gateway/registry"
	"devshard/heightsync"
)

// HeightSyncSource is the live escrow set and the session of each; the collector reads the cached view and never builds a fresh one on a scrape.
type HeightSyncSource interface {
	EscrowSource
	RoutableSession(escrowID string) (registry.EscrowSession, bool)
}

// HeightSyncCollector reports each live escrow's cached height-sync view.
type HeightSyncCollector struct {
	source HeightSyncSource

	secondsSinceContact  *prometheus.Desc
	armingPredicted      *prometheus.Desc
	hostHeight           *prometheus.Desc
	hostHeightLag        *prometheus.Desc
	heightSpread         *prometheus.Desc
	hostClaimAge         *prometheus.Desc
	hostSyncState        *prometheus.Desc
	cadenceEvents        *prometheus.Desc
	secondsSinceTurnover *prometheus.Desc
	turnsAbandoned       *prometheus.Desc
	anchorsLastBlock     *prometheus.Desc
	anchorsPerBlock      *prometheus.Desc
	turnoversPerBlock    *prometheus.Desc
	blocksWithoutAnchor  *prometheus.Desc
	peerSeenCount        *prometheus.Desc
	peerSeenUnseen       *prometheus.Desc
	exchanges            *prometheus.Desc
	gatewayTip           *prometheus.Desc
}

func NewHeightSyncCollector(source HeightSyncSource) *HeightSyncCollector {
	return &HeightSyncCollector{
		source:               source,
		secondsSinceContact:  gaugeDesc("devshard_gateway_heightsync_seconds_since_contact", "Seconds since this gateway last sent a signed diff to the slot or received a tx of its own back.", "devshard_id", "slot"),
		armingPredicted:      gaugeDesc("devshard_gateway_heightsync_arming_predicted", "1 when seconds_since_contact exceeds the idle timeout. Prediction only; never drives closing.", "devshard_id", "slot"),
		hostHeight:           gaugeDesc("devshard_gateway_heightsync_host_height", "First-party host height claim from the response-leg Anchor (host-reported timestamp; not attested).", "devshard_id", "slot", "participant_key"),
		hostHeightLag:        gaugeDesc("devshard_gateway_heightsync_host_height_lag", "Highest fresh claim minus this slot's claim, over the same fresh slots host_height is emitted for. 0 is the leader. Its maximum is at most height_spread, which also counts stale claims.", "devshard_id", "slot"),
		heightSpread:         gaugeDesc("devshard_gateway_heightsync_height_spread", "Highest claim minus lowest claim over the slots that still have a height claim, stale ones included. Freshness shows as host_height disappearing and in host_claim_age_seconds, not as this gauge shrinking.", "devshard_id"),
		hostClaimAge:         gaugeDesc("devshard_gateway_heightsync_host_claim_age_seconds", "Age of the slot's last first-party height claim (host-reported timestamp; not attested).", "devshard_id", "slot"),
		hostSyncState:        gaugeDesc("devshard_gateway_heightsync_host_sync_state", "Last sync_state self-report from a slot's ack (1 = current state).", "devshard_id", "slot", "state"),
		cadenceEvents:        counterDesc("devshard_gateway_heightsync_cadence_events_total", "Heartbeat due-check dispositions, including heartbeats replaced by inference stamps.", "devshard_id", "event"),
		secondsSinceTurnover: gaugeDesc("devshard_gateway_heightsync_seconds_since_turnover", "Seconds since the last full height-sync turnover.", "devshard_id"),
		turnsAbandoned:       counterDesc("devshard_gateway_heightsync_turns_abandoned_total", "Heartbeat turns reopened after the turn timeout without quorum.", "devshard_id"),
		anchorsLastBlock:     gaugeDesc("devshard_gateway_heightsync_anchors_last_block", "Anchor counts by kind for the most recently sealed height.", "devshard_id", "kind"),
		anchorsPerBlock:      prometheus.NewDesc("devshard_gateway_heightsync_anchors_per_block", "Distribution of anchors per sealed height. Empty heights skipped by fast-forward appear only in blocks_without_anchor_total, not as zero samples.", []string{"devshard_id"}, nil),
		turnoversPerBlock:    prometheus.NewDesc("devshard_gateway_heightsync_turnovers_per_block", "Distribution of full turnovers per sealed height. Same sampling rule as anchors_per_block.", []string{"devshard_id"}, nil),
		blocksWithoutAnchor:  counterDesc("devshard_gateway_heightsync_blocks_without_anchor_total", "Sealed heights with zero anchors, including empty ranges skipped by seal fast-forward.", "devshard_id"),
		peerSeenCount:        gaugeDesc("devshard_gateway_heightsync_peer_seen_count", "Popcount of one observer's peer_seen bitmap.", "devshard_id", "observer_slot"),
		peerSeenUnseen:       gaugeDesc("devshard_gateway_heightsync_peer_seen_unseen_total", "How many observers do not currently see this slot.", "devshard_id", "subject_slot"),
		exchanges:            counterDesc("devshard_gateway_heightsync_exchanges_total", "Per-exchange stamp and section overlap that gates making the envelope section optional.", "devshard_id", "kind"),
		gatewayTip:           gaugeDesc("devshard_gateway_heightsync_gateway_tip", "This gateway's own oracle tip, kept apart from host claims so an operator can tell host disagreement from a lagging sequencer.", "devshard_id"),
	}
}

func (c *HeightSyncCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{
		c.secondsSinceContact, c.armingPredicted, c.hostHeight, c.hostHeightLag, c.heightSpread, c.hostClaimAge,
		c.hostSyncState, c.cadenceEvents, c.secondsSinceTurnover, c.turnsAbandoned, c.anchorsLastBlock,
		c.anchorsPerBlock, c.turnoversPerBlock, c.blocksWithoutAnchor, c.peerSeenCount, c.peerSeenUnseen,
		c.exchanges, c.gatewayTip,
	} {
		ch <- desc
	}
}

func (c *HeightSyncCollector) Collect(ch chan<- prometheus.Metric) {
	if c.source == nil {
		return
	}
	for _, escrow := range c.source.Snapshot() {
		session, live := c.source.RoutableSession(escrow.ID)
		if !live {
			continue
		}
		view := session.HeightSyncView()
		if unpublished(view) {
			continue
		}
		c.collectView(ch, view, metricLabel(view.DevshardID, escrow.ID))
	}
}

func unpublished(view heightsync.OperatorView) bool {
	return len(view.Slots) == 0 && len(view.Tips) == 0 && len(view.CadenceCounts) == 0 && view.Overlap.Total == 0 && view.GatewayTip == 0
}

func (c *HeightSyncCollector) collectView(ch chan<- prometheus.Metric, view heightsync.OperatorView, id string) {
	c.collectContacts(ch, view, id)
	c.collectTips(ch, view, id)
	c.collectSyncStates(ch, view, id)
	c.collectCadence(ch, view, id)
	c.collectAnchors(ch, view, id)
	c.collectPeers(ch, view, id)
	if view.GatewayTip > 0 {
		gauge(ch, c.gatewayTip, float64(view.GatewayTip), id)
	}
	counter(ch, c.exchanges, float64(view.Overlap.Total), id, exchangeTotal)
	counter(ch, c.exchanges, float64(view.Overlap.WithSection), id, exchangeWithSection)
	counter(ch, c.exchanges, float64(view.Overlap.WithStamp), id, exchangeWithStamp)
	counter(ch, c.exchanges, float64(view.Overlap.Agreed), id, exchangeAgreed)
}

func (c *HeightSyncCollector) collectContacts(ch chan<- prometheus.Metric, view heightsync.OperatorView, id string) {
	for _, contact := range view.Contacts {
		if contact.LastAt.IsZero() {
			continue
		}
		slot := slotLabel(contact.Slot)
		gauge(ch, c.secondsSinceContact, contact.SinceContact.Seconds(), id, slot)
		gauge(ch, c.armingPredicted, boolGauge(view.IdleTimeout > 0 && contact.SinceContact > view.IdleTimeout), id, slot)
	}
}

func (c *HeightSyncCollector) collectTips(ch chan<- prometheus.Metric, view heightsync.OperatorView, id string) {
	var maxClaim, minClaim, maxFresh uint64
	claimed := false
	for _, tip := range view.Tips {
		if tip.Height == 0 {
			continue
		}
		if !claimed || tip.Height > maxClaim {
			maxClaim = tip.Height
		}
		if !claimed || tip.Height < minClaim {
			minClaim = tip.Height
		}
		claimed = true
		if tip.Fresh && tip.Height > maxFresh {
			maxFresh = tip.Height
		}
		if tip.AgeKnown {
			gauge(ch, c.hostClaimAge, tip.Age.Seconds(), id, slotLabel(tip.Slot))
		}
	}
	for _, tip := range view.Tips {
		if !tip.Fresh || tip.Height == 0 {
			continue
		}
		slot := slotLabel(tip.Slot)
		gauge(ch, c.hostHeight, float64(tip.Height), id, slot, metricLabel(tip.Originator, labelUnknown))
		gauge(ch, c.hostHeightLag, float64(maxFresh-tip.Height), id, slot)
	}
	if claimed {
		gauge(ch, c.heightSpread, float64(maxClaim-minClaim), id)
	}
}

func (c *HeightSyncCollector) collectSyncStates(ch chan<- prometheus.Metric, view heightsync.OperatorView, id string) {
	for _, reported := range view.SyncStates {
		for _, state := range syncStates {
			gauge(ch, c.hostSyncState, boolGauge(reported.State == state), id, slotLabel(reported.Slot), state)
		}
	}
}

func (c *HeightSyncCollector) collectCadence(ch chan<- prometheus.Metric, view heightsync.OperatorView, id string) {
	for event, count := range view.CadenceCounts {
		counter(ch, c.cadenceEvents, float64(count), id, event)
	}
	gauge(ch, c.secondsSinceTurnover, view.SecondsSinceTurnover, id)
	counter(ch, c.turnsAbandoned, float64(view.AbandonedTurns), id)
}

func (c *HeightSyncCollector) collectAnchors(ch chan<- prometheus.Metric, view heightsync.OperatorView, id string) {
	if view.AnchorsLastSealed != nil {
		for _, kind := range anchorKinds {
			gauge(ch, c.anchorsLastBlock, float64(view.AnchorsLastSealed.ByKind[kind]), id, kind)
		}
	}
	histogram(ch, c.anchorsPerBlock, view.AnchorsPerBlock, id)
	histogram(ch, c.turnoversPerBlock, view.TurnoversPerBlock, id)
	counter(ch, c.blocksWithoutAnchor, float64(view.BlocksWithoutAnchor), id)
}

func (c *HeightSyncCollector) collectPeers(ch chan<- prometheus.Metric, view heightsync.OperatorView, id string) {
	slots := uint32(len(view.Slots))
	if slots == 0 {
		for _, row := range view.PeerSeen {
			slots = max(slots, uint32(len(row.Bits))*bitsPerByte)
		}
	}
	unseen := make([]int, slots)
	for _, row := range view.PeerSeen {
		gauge(ch, c.peerSeenCount, float64(row.Count), id, slotLabel(row.Observer))
		for subject := range slots {
			if !heightsync.PeerSeenBit(row.Bits, subject) {
				unseen[subject]++
			}
		}
	}
	if len(view.PeerSeen) == 0 {
		return
	}
	for subject, observers := range unseen {
		gauge(ch, c.peerSeenUnseen, float64(observers), id, slotLabel(uint32(subject)))
	}
}

func slotLabel(slot uint32) string { return strconv.FormatUint(uint64(slot), 10) }

func histogram(ch chan<- prometheus.Metric, desc *prometheus.Desc, snapshot heightsync.HistogramSnapshot, labelValues ...string) {
	if snapshot.Count == 0 && snapshot.Sum == 0 {
		return
	}
	ch <- prometheus.MustNewConstHistogram(desc, snapshot.Count, snapshot.Sum, snapshot.Buckets, labelValues...)
}
