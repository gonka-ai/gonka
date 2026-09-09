package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"devshard/heightsync"
)

const envHeightSyncPeerMatrix = "DEVSHARD_GATEWAY_HEIGHTSYNC_PEER_MATRIX"

type heightSyncDescs struct {
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
	peerSeen             *prometheus.Desc
	peerSeenCount        *prometheus.Desc
	peerSeenUnseen       *prometheus.Desc
	overlap              *prometheus.Desc
	gatewayTip           *prometheus.Desc
}

func newHeightSyncDescs() heightSyncDescs {
	return heightSyncDescs{
		secondsSinceContact: prometheus.NewDesc(
			"devshard_gateway_heightsync_seconds_since_contact",
			"Seconds since this gateway last sent a signed diff to the slot or received a tx of its own back.",
			[]string{"devshard_id", "slot"}, nil,
		),
		armingPredicted: prometheus.NewDesc(
			"devshard_gateway_heightsync_arming_predicted",
			"1 when seconds_since_contact exceeds T_idle. Prediction only; never drives closing.",
			[]string{"devshard_id", "slot"}, nil,
		),
		hostHeight: prometheus.NewDesc(
			"devshard_gateway_heightsync_host_height",
			"First-party host height claim from the response-leg Anchor (host-reported timestamp; not attested).",
			[]string{"devshard_id", "slot", "participant_key"}, nil,
		),
		hostHeightLag: prometheus.NewDesc(
			"devshard_gateway_heightsync_host_height_lag",
			"Highest fresh claim − this slot's claim, over the same fresh slots host_height is emitted for. 0 is the leader. Its maximum is the spread across fresh slots only, so it is at most height_spread, which also counts stale claims.",
			[]string{"devshard_id", "slot"}, nil,
		),
		heightSpread: prometheus.NewDesc(
			"devshard_gateway_heightsync_height_spread",
			"max_claim − min_claim over slots that still have a height claim (including stale). Freshness is signalled by host_height disappearance and host_claim_age_seconds, not by shrinking this gauge.",
			[]string{"devshard_id"}, nil,
		),
		hostClaimAge: prometheus.NewDesc(
			"devshard_gateway_heightsync_host_claim_age_seconds",
			"Age of the slot's last first-party height claim (host-reported timestamp; not attested).",
			[]string{"devshard_id", "slot"}, nil,
		),
		hostSyncState: prometheus.NewDesc(
			"devshard_gateway_heightsync_host_sync_state",
			"Last sync_state self-report from a slot's ack (1 = current state).",
			[]string{"devshard_id", "slot", "state"}, nil,
		),
		cadenceEvents: prometheus.NewDesc(
			"devshard_gateway_heightsync_cadence_events_total",
			"Heartbeat due-check dispositions, including heartbeats replaced by inference stamps.",
			[]string{"devshard_id", "event"}, nil,
		),
		secondsSinceTurnover: prometheus.NewDesc(
			"devshard_gateway_heightsync_seconds_since_turnover",
			"Seconds since the last full height-sync turnover.",
			[]string{"devshard_id"}, nil,
		),
		turnsAbandoned: prometheus.NewDesc(
			"devshard_gateway_heightsync_turns_abandoned_total",
			"Heartbeat turns reopened after TurnTimeout without quorum.",
			[]string{"devshard_id"}, nil,
		),
		anchorsLastBlock: prometheus.NewDesc(
			"devshard_gateway_heightsync_anchors_last_block",
			"Anchor counts by kind for the most recently sealed height.",
			[]string{"devshard_id", "kind"}, nil,
		),
		anchorsPerBlock: prometheus.NewDesc(
			"devshard_gateway_heightsync_anchors_per_block",
			"Distribution of anchors per sealed height. Histogram count is sealed-with-detail plus folded open buckets; empty heights skipped by fast-forward appear only in blocks_without_anchor_total (not as observe(0) samples).",
			[]string{"devshard_id"}, nil,
		),
		turnoversPerBlock: prometheus.NewDesc(
			"devshard_gateway_heightsync_turnovers_per_block",
			"Distribution of full turnovers per sealed height. Same sampling rule as anchors_per_block: fast-forward empties are not histogram zeros.",
			[]string{"devshard_id"}, nil,
		),
		blocksWithoutAnchor: prometheus.NewDesc(
			"devshard_gateway_heightsync_blocks_without_anchor_total",
			"Sealed heights with zero anchors, including empty ranges skipped by seal fast-forward.",
			[]string{"devshard_id"}, nil,
		),
		peerSeen: prometheus.NewDesc(
			"devshard_gateway_heightsync_peer_seen",
			"1 if observer_slot reported seeing subject_slot. Opt-in via DEVSHARD_GATEWAY_HEIGHTSYNC_PEER_MATRIX.",
			[]string{"devshard_id", "observer_slot", "subject_slot"}, nil,
		),
		peerSeenCount: prometheus.NewDesc(
			"devshard_gateway_heightsync_peer_seen_count",
			"Popcount of one observer's peer_seen bitmap.",
			[]string{"devshard_id", "observer_slot"}, nil,
		),
		peerSeenUnseen: prometheus.NewDesc(
			"devshard_gateway_heightsync_peer_seen_unseen_total",
			"How many observers do not currently see this slot.",
			[]string{"devshard_id", "subject_slot"}, nil,
		),
		overlap: prometheus.NewDesc(
			"devshard_gateway_heightsync_exchanges_total",
			"Per-exchange stamp/section overlap that gates making the envelope section optional.",
			[]string{"devshard_id", "kind"}, nil,
		),
		gatewayTip: prometheus.NewDesc(
			"devshard_gateway_heightsync_gateway_tip",
			"This gateway's own oracle tip. Separate from host claims so operators can tell host disagreement from a lagging sequencer.",
			[]string{"devshard_id"}, nil,
		),
	}
}

func heightSyncPeerMatrixEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envHeightSyncPeerMatrix))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (d heightSyncDescs) describe(ch chan<- *prometheus.Desc) {
	ch <- d.secondsSinceContact
	ch <- d.armingPredicted
	ch <- d.hostHeight
	ch <- d.hostHeightLag
	ch <- d.heightSpread
	ch <- d.hostClaimAge
	ch <- d.hostSyncState
	ch <- d.cadenceEvents
	ch <- d.secondsSinceTurnover
	ch <- d.turnsAbandoned
	ch <- d.anchorsLastBlock
	ch <- d.anchorsPerBlock
	ch <- d.turnoversPerBlock
	ch <- d.blocksWithoutAnchor
	ch <- d.peerSeen
	ch <- d.peerSeenCount
	ch <- d.peerSeenUnseen
	ch <- d.overlap
	ch <- d.gatewayTip
}

func (rt *devshardRuntime) heightSyncView() heightsync.OperatorView {
	if rt == nil {
		return heightsync.OperatorView{}
	}
	if rt.testHeightSyncView != nil {
		v := *rt.testHeightSyncView
		if v.DevshardID == "" {
			v.DevshardID = rt.id
		}
		return v
	}
	if rt.session == nil || !rt.session.HeightSyncWired() {
		return heightsync.OperatorView{}
	}
	// Cache only — never SnapshotHeightSync on the scrape path.
	v := rt.session.CachedHeightSyncView()
	if len(v.Slots) == 0 && len(v.Tips) == 0 && len(v.CadenceCounts) == 0 && v.Overlap.Total == 0 && v.GatewayTip == 0 {
		// Wired but not yet published (e.g. between SetHeightSyncCadence and
		// the first publish). Skip rather than invent series from rt.id alone.
		return heightsync.OperatorView{}
	}
	if v.DevshardID == "" {
		v.DevshardID = rt.id
	}
	return v
}

func (c *gatewayMetricsCollector) collectHeightSync(ch chan<- prometheus.Metric, runtimes []*devshardRuntime) {
	if c == nil || c.heightSync.secondsSinceContact == nil {
		return
	}
	peerMatrix := c.peerMatrix
	for _, rt := range runtimes {
		if rt == nil {
			continue
		}
		view := rt.heightSyncView()
		// Empty view: unwired session, nil session, or not yet published.
		if view.DevshardID == "" && len(view.Slots) == 0 && len(view.Tips) == 0 && len(view.CadenceCounts) == 0 {
			continue
		}
		id := metricLabel(view.DevshardID, rt.id)
		emitHeightSyncView(ch, c.heightSync, view, id, peerMatrix)
	}
}

func emitHeightSyncView(ch chan<- prometheus.Metric, d heightSyncDescs, view heightsync.OperatorView, id string, peerMatrix bool) {
	slotLabel := func(slot uint32) string { return strconv.FormatUint(uint64(slot), 10) }

	idle := view.IdleTimeout
	for _, c := range view.Contacts {
		ch <- prometheus.MustNewConstMetric(d.secondsSinceContact, prometheus.GaugeValue, c.SinceContact.Seconds(), id, slotLabel(c.Slot))
		predicted := 0.0
		if idle > 0 && c.SinceContact > idle && !c.LastAt.IsZero() {
			predicted = 1
		}
		ch <- prometheus.MustNewConstMetric(d.armingPredicted, prometheus.GaugeValue, predicted, id, slotLabel(c.Slot))
	}

	var maxClaim, minClaim uint64
	var haveClaim bool
	var maxFresh uint64
	var haveFresh bool
	for _, t := range view.Tips {
		if t.Height == 0 {
			continue
		}
		if !haveClaim || t.Height > maxClaim {
			maxClaim = t.Height
		}
		if !haveClaim || t.Height < minClaim {
			minClaim = t.Height
		}
		haveClaim = true
		if t.Fresh {
			if !haveFresh || t.Height > maxFresh {
				maxFresh = t.Height
			}
			haveFresh = true
		}
		ch <- prometheus.MustNewConstMetric(d.hostClaimAge, prometheus.GaugeValue, t.Age.Seconds(), id, slotLabel(t.Slot))
	}
	for _, t := range view.Tips {
		if !t.Fresh || t.Height == 0 {
			continue
		}
		ch <- prometheus.MustNewConstMetric(d.hostHeight, prometheus.GaugeValue, float64(t.Height), id, slotLabel(t.Slot), metricLabel(t.Originator, "unknown"))
		ch <- prometheus.MustNewConstMetric(d.hostHeightLag, prometheus.GaugeValue, float64(maxFresh-t.Height), id, slotLabel(t.Slot))
	}
	if haveClaim {
		ch <- prometheus.MustNewConstMetric(d.heightSpread, prometheus.GaugeValue, float64(maxClaim-minClaim), id)
	}
	if view.GatewayTip > 0 {
		ch <- prometheus.MustNewConstMetric(d.gatewayTip, prometheus.GaugeValue, float64(view.GatewayTip), id)
	}

	states := []string{"SYNCED", "CATCHING_UP", "ORACLE_STALE", "ORACLE_UNAVAILABLE", "SYNC_STATE_UNSPECIFIED"}
	for _, ss := range view.SyncStates {
		for _, st := range states {
			v := 0.0
			if ss.State == st {
				v = 1
			}
			ch <- prometheus.MustNewConstMetric(d.hostSyncState, prometheus.GaugeValue, v, id, slotLabel(ss.Slot), st)
		}
	}

	for event, n := range view.CadenceCounts {
		ch <- prometheus.MustNewConstMetric(d.cadenceEvents, prometheus.CounterValue, float64(n), id, event)
	}
	ch <- prometheus.MustNewConstMetric(d.secondsSinceTurnover, prometheus.GaugeValue, view.SecondsSinceTurnover, id)
	ch <- prometheus.MustNewConstMetric(d.turnsAbandoned, prometheus.CounterValue, float64(view.AbandonedTurns), id)

	if view.AnchorsLastSealed != nil {
		kinds := []string{heightsync.AnchorKindCadence, heightsync.AnchorKindHeartbeat, heightsync.AnchorKindForced, heightsync.AnchorKindResponse}
		for _, k := range kinds {
			ch <- prometheus.MustNewConstMetric(d.anchorsLastBlock, prometheus.GaugeValue, float64(view.AnchorsLastSealed.ByKind[k]), id, k)
		}
	}
	emitHist(ch, d.anchorsPerBlock, view.AnchorsPerBlock, id)
	emitHist(ch, d.turnoversPerBlock, view.TurnoversPerBlock, id)
	ch <- prometheus.MustNewConstMetric(d.blocksWithoutAnchor, prometheus.CounterValue, float64(view.BlocksWithoutAnchor), id)

	ch <- prometheus.MustNewConstMetric(d.overlap, prometheus.CounterValue, float64(view.Overlap.Total), id, "total")
	ch <- prometheus.MustNewConstMetric(d.overlap, prometheus.CounterValue, float64(view.Overlap.WithSection), id, "section")
	ch <- prometheus.MustNewConstMetric(d.overlap, prometheus.CounterValue, float64(view.Overlap.WithStamp), id, "stamp")
	ch <- prometheus.MustNewConstMetric(d.overlap, prometheus.CounterValue, float64(view.Overlap.Agreed), id, "agree")

	slotsNum := uint32(len(view.Slots))
	if slotsNum == 0 {
		for _, row := range view.PeerSeen {
			if uint32(len(row.Bits)*8) > slotsNum {
				slotsNum = uint32(len(row.Bits) * 8)
			}
		}
	}
	unseen := make([]int, slotsNum)
	seenAny := false
	for _, row := range view.PeerSeen {
		seenAny = true
		ch <- prometheus.MustNewConstMetric(d.peerSeenCount, prometheus.GaugeValue, float64(row.Count), id, slotLabel(row.Observer))
		for sub := uint32(0); sub < slotsNum; sub++ {
			bit := heightsync.PeerSeenBit(row.Bits, sub)
			if !bit {
				if int(sub) < len(unseen) {
					unseen[sub]++
				}
			}
			if peerMatrix {
				v := 0.0
				if bit {
					v = 1
				}
				ch <- prometheus.MustNewConstMetric(d.peerSeen, prometheus.GaugeValue, v, id, slotLabel(row.Observer), slotLabel(sub))
			}
		}
	}
	if seenAny {
		for sub := uint32(0); sub < slotsNum; sub++ {
			ch <- prometheus.MustNewConstMetric(d.peerSeenUnseen, prometheus.GaugeValue, float64(unseen[sub]), id, slotLabel(sub))
		}
	}
}

func emitHist(ch chan<- prometheus.Metric, desc *prometheus.Desc, snap heightsync.HistogramSnapshot, id string) {
	if snap.Count == 0 && snap.Sum == 0 {
		return
	}
	ch <- prometheus.MustNewConstHistogram(desc, snap.Count, snap.Sum, snap.Buckets, id)
}
