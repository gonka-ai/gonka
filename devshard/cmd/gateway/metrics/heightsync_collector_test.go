package metrics

import (
	"maps"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"devshard/cmd/gateway/registry"
	"devshard/heightsync"
)

type viewSession struct {
	registry.EscrowSession
	view heightsync.OperatorView
}

func (s viewSession) HeightSyncView() heightsync.OperatorView { return s.view }

type heightSyncEscrows struct {
	fixedEscrows
	sessions map[string]registry.EscrowSession
}

func (h heightSyncEscrows) RoutableSession(escrowID string) (registry.EscrowSession, bool) {
	session, live := h.sessions[escrowID]
	return session, live
}

func publishedHeightSyncView() heightsync.OperatorView {
	return heightsync.OperatorView{
		DevshardID:  "7",
		IdleTimeout: 20 * time.Second,
		GatewayTip:  105,
		Slots:       []heightsync.SlotIdentity{{}, {}, {}},
		Tips: []heightsync.OriginTip{
			{Slot: 0, Originator: "gonka1zero", Height: 100, Age: 2 * time.Second, AgeKnown: true, Fresh: true},
			{Slot: 1, Originator: "gonka1one", Height: 98, Age: 4 * time.Second, AgeKnown: true, Fresh: true},
			{Slot: 2, Originator: "gonka1two", Height: 90, Age: 300 * time.Second, AgeKnown: true},
			{Slot: 3, Originator: "gonka1three", Height: 95, Age: time.Second},
		},
		Contacts: []heightsync.SlotContact{
			{Slot: 0, SinceContact: 30 * time.Second, LastAt: time.Unix(1_700_000_000, 0)},
			{Slot: 1, SinceContact: 5 * time.Second, LastAt: time.Unix(1_700_000_025, 0)},
			{Slot: 2},
		},
		CadenceCounts:        map[string]uint64{"due": 4, "stamped": 2},
		AbandonedTurns:       2,
		SecondsSinceTurnover: 12.5,
		PeerSeen:             []heightsync.PeerSeenRow{{Observer: 0, Bits: []byte{0b011}, Count: 2}, {Observer: 1, Bits: []byte{0b001}, Count: 1}},
		SyncStates:           []heightsync.SlotSyncState{{Slot: 0, State: "SYNCED"}},
		AnchorsLastSealed:    &heightsync.SealedAnchorCounts{ByKind: map[string]int{heightsync.AnchorKindCadence: 3, heightsync.AnchorKindHeartbeat: 1}},
		AnchorsPerBlock:      heightsync.HistogramSnapshot{Count: 3, Sum: 5, Buckets: map[float64]uint64{1: 1, 2: 3}},
		TurnoversPerBlock:    heightsync.HistogramSnapshot{Count: 2, Sum: 2, Buckets: map[float64]uint64{1: 2}},
		BlocksWithoutAnchor:  6,
		Overlap:              heightsync.ExchangeOverlap{Total: 10, WithSection: 8, WithStamp: 7, Agreed: 5},
	}
}

// Test flow:
//  1. Register a `HeightSyncCollector` over one live escrow whose session has published a view with three slots, two fresh host claims, one stale and one whose host published no observation time, two contacts and one slot never contacted, a sync state, cadence counts, anchors, two peer rows and overlap counts.
//  2. Gather the registry.
//  3. Assert each family reports what the view holds: contact age and the arming prediction, claims with their lag, an age only for the claims whose host published a time, no contact age for a slot never contacted, and the spread over stale claims too, sync state one-hot, cadence and exchange counters, anchors, both histograms, peer counts and the unseen count per slot.
func TestTheHeightSyncCollectorReportsWhatTheViewHolds(t *testing.T) {
	telemetry := New()
	telemetry.Register(NewHeightSyncCollector(heightSyncEscrows{
		fixedEscrows: fixedEscrows{states: []registry.EscrowState{{ID: "7"}}},
		sessions:     map[string]registry.EscrowSession{"7": viewSession{view: publishedHeightSyncView()}},
	}))
	escrow := func(extra labels) labels {
		merged := labels{"devshard_id": "7"}
		maps.Copy(merged, extra)
		return merged
	}

	expectGauge(t, telemetry, "devshard_gateway_heightsync_seconds_since_contact", escrow(labels{"slot": "0"}), 30)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_arming_predicted", escrow(labels{"slot": "0"}), 1)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_arming_predicted", escrow(labels{"slot": "1"}), 0)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_host_height", escrow(labels{"slot": "0", "participant_key": "gonka1zero"}), 100)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_host_height_lag", escrow(labels{"slot": "1"}), 2)
	expectSeriesCount(t, telemetry, "devshard_gateway_heightsync_host_height", 2)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_host_claim_age_seconds", escrow(labels{"slot": "2"}), 300)
	expectSeriesCount(t, telemetry, "devshard_gateway_heightsync_host_claim_age_seconds", 3)
	expectSeriesCount(t, telemetry, "devshard_gateway_heightsync_seconds_since_contact", 2)
	expectSeriesCount(t, telemetry, "devshard_gateway_heightsync_arming_predicted", 2)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_height_spread", escrow(nil), 10)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_gateway_tip", escrow(nil), 105)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_host_sync_state", escrow(labels{"slot": "0", "state": "SYNCED"}), 1)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_host_sync_state", escrow(labels{"slot": "0", "state": "CATCHING_UP"}), 0)
	expectCounter(t, telemetry, "devshard_gateway_heightsync_cadence_events_total", escrow(labels{"event": "due"}), 4)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_seconds_since_turnover", escrow(nil), 12.5)
	expectCounter(t, telemetry, "devshard_gateway_heightsync_turns_abandoned_total", escrow(nil), 2)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_anchors_last_block", escrow(labels{"kind": "cadence"}), 3)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_anchors_last_block", escrow(labels{"kind": "forced"}), 0)
	expectHistogram(t, telemetry, "devshard_gateway_heightsync_anchors_per_block", escrow(nil), 3, 5)
	expectHistogram(t, telemetry, "devshard_gateway_heightsync_turnovers_per_block", escrow(nil), 2, 2)
	expectCounter(t, telemetry, "devshard_gateway_heightsync_blocks_without_anchor_total", escrow(nil), 6)
	expectCounter(t, telemetry, "devshard_gateway_heightsync_exchanges_total", escrow(labels{"kind": "total"}), 10)
	expectCounter(t, telemetry, "devshard_gateway_heightsync_exchanges_total", escrow(labels{"kind": "agree"}), 5)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_peer_seen_count", escrow(labels{"observer_slot": "0"}), 2)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_peer_seen_unseen_total", escrow(labels{"subject_slot": "0"}), 0)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_peer_seen_unseen_total", escrow(labels{"subject_slot": "1"}), 1)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_peer_seen_unseen_total", escrow(labels{"subject_slot": "2"}), 2)
	expectSeriesCount(t, telemetry, "devshard_gateway_heightsync_peer_seen_unseen_total", 3)
}

// Test flow:
//  1. Register a `HeightSyncCollector` over three escrows: one whose session has published nothing, one with no live session, and one that is live and published.
//  2. Gather the gateway tip family, which every published view with a tip reports.
//  3. Assert only the published escrow has a series, so an unwired or unpublished session invents no series from its id.
func TestTheHeightSyncCollectorSkipsEscrowsWithNothingPublished(t *testing.T) {
	telemetry := New()
	telemetry.Register(NewHeightSyncCollector(heightSyncEscrows{
		fixedEscrows: fixedEscrows{states: []registry.EscrowState{{ID: "1"}, {ID: "2"}, {ID: "3"}}},
		sessions: map[string]registry.EscrowSession{
			"1": viewSession{},
			"3": viewSession{view: publishedHeightSyncView()},
		},
	}))

	expectSeriesCount(t, telemetry, "devshard_gateway_heightsync_gateway_tip", 1)
	expectGauge(t, telemetry, "devshard_gateway_heightsync_gateway_tip", labels{"devshard_id": "7"}, 105)
}

// Test flow:
//  1. Register a `HeightSyncCollector` with no source.
//  2. Assert the gateway-tip family has no series.
func TestAHeightSyncCollectorWithNoSourceReportsNothing(t *testing.T) {
	telemetry := New()
	telemetry.Register(NewHeightSyncCollector(nil))

	expectSeriesCount(t, telemetry, "devshard_gateway_heightsync_gateway_tip", 0)
}

// Test flow:
//  1. Register a `HeightSyncCollector` over one live escrow with a published view that holds peer rows.
//  2. Gather every family and assert none of the height-sync series carries both an observer slot and a subject slot.
//  3. Drop the escrow's live session and gather again.
//  4. Assert no height-sync family is left in the registry.
func TestHeightSyncSeriesCarryNoSlotPairAndVanishWithTheSession(t *testing.T) {
	sessions := map[string]registry.EscrowSession{"7": viewSession{view: publishedHeightSyncView()}}
	telemetry := New()
	telemetry.Register(NewHeightSyncCollector(heightSyncEscrows{
		fixedEscrows: fixedEscrows{states: []registry.EscrowState{{ID: "7"}}},
		sessions:     sessions,
	}))

	heightSyncFamilies := func() []*dto.MetricFamily {
		gathered, err := telemetry.Registry().Gather()
		if err != nil {
			t.Fatalf("gather: %v", err)
		}
		var found []*dto.MetricFamily
		for _, gatheredFamily := range gathered {
			if strings.HasPrefix(gatheredFamily.GetName(), "devshard_gateway_heightsync_") {
				found = append(found, gatheredFamily)
			}
		}
		return found
	}

	live := heightSyncFamilies()
	if len(live) == 0 {
		t.Fatal("a live escrow with a published view emitted no height-sync family")
	}
	for _, gatheredFamily := range live {
		for _, metric := range gatheredFamily.GetMetric() {
			present := map[string]bool{}
			for _, pair := range metric.GetLabel() {
				present[pair.GetName()] = true
			}
			if present["observer_slot"] && present["subject_slot"] {
				t.Errorf("%s carries both observer_slot and subject_slot", gatheredFamily.GetName())
			}
		}
	}

	delete(sessions, "7")
	for _, gatheredFamily := range heightSyncFamilies() {
		t.Errorf("%s survives the escrow's live session", gatheredFamily.GetName())
	}
}
