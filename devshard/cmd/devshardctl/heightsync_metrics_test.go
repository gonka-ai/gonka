package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/stub"
	"devshard/transport"
	"devshard/user"
)

func gatherHeightSync(t *testing.T, g *Gateway, peerMatrix bool) []*dto.MetricFamily {
	t.Helper()
	collector := newGatewayMetricsCollectorWithHostConnections(g, fakeHostConnectionSnapshotter(nil))
	collector.peerMatrix = peerMatrix
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	families, err := registry.Gather()
	assert.NoError(t, err)
	return families
}

func threeSlotView(id string) heightsync.OperatorView {
	return heightsync.OperatorView{
		DevshardID:  id,
		IdleTimeout: heightsync.DefaultHeartbeatIdleTimeout,
		Freshness:   heightsync.DefaultOriginatorFreshness,
		Slots: []heightsync.SlotIdentity{
			{Slot: 0, ParticipantKey: "host-a"},
			{Slot: 1, ParticipantKey: "host-b"},
			{Slot: 2, ParticipantKey: "host-c"},
		},
	}
}

func TestGatewayHeightSync_DivergenceSpreadAndLag(t *testing.T) {
	view := threeSlotView("12")
	view.Tips = []heightsync.OriginTip{
		{Slot: 0, Originator: "host-a", Height: 100, Fresh: true},
		{Slot: 1, Originator: "host-b", Height: 100, Fresh: true},
		{Slot: 2, Originator: "host-c", Height: 95, Fresh: true},
	}
	g := &Gateway{runtimeOrder: []*devshardRuntime{{id: "12", testHeightSyncView: &view}}}
	families := gatherHeightSync(t, g, false)
	requireMetricGaugeValue(t, families, "devshard_gateway_heightsync_height_spread", map[string]string{"devshard_id": "12"}, 5)
	requireMetricGaugeValue(t, families, "devshard_gateway_heightsync_host_height_lag", map[string]string{"devshard_id": "12", "slot": "0"}, 0)
	requireMetricGaugeValue(t, families, "devshard_gateway_heightsync_host_height_lag", map[string]string{"devshard_id": "12", "slot": "1"}, 0)
	requireMetricGaugeValue(t, families, "devshard_gateway_heightsync_host_height_lag", map[string]string{"devshard_id": "12", "slot": "2"}, 5)
}

func TestGatewayHeightSync_StaleClaimDropsFromSpread(t *testing.T) {
	// Stale slot drops host_height and raises claim age; spread keeps the
	// last known claim so the alertable number does not silently shrink.
	view := threeSlotView("12")
	view.Tips = []heightsync.OriginTip{
		{Slot: 0, Originator: "host-a", Height: 100, Fresh: true},
		{Slot: 1, Originator: "host-b", Height: 100, Fresh: true},
		{Slot: 2, Originator: "host-c", Height: 95, Age: 90 * time.Second, Fresh: false},
	}
	g := &Gateway{runtimeOrder: []*devshardRuntime{{id: "12", testHeightSyncView: &view}}}
	families := gatherHeightSync(t, g, false)
	requireMetricGaugeValue(t, families, "devshard_gateway_heightsync_height_spread", map[string]string{"devshard_id": "12"}, 5)
	requireMetricGaugeValue(t, families, "devshard_gateway_heightsync_host_claim_age_seconds", map[string]string{"devshard_id": "12", "slot": "2"}, 90)
	requireNoMetricWithLabels(t, families, "devshard_gateway_heightsync_host_height", map[string]string{"devshard_id": "12", "slot": "2"})
}

func TestGatewayHeightSync_QuietCadenceEventsAndRing(t *testing.T) {
	view := threeSlotView("12")
	view.CadenceCounts = map[string]uint64{string(heightsync.CadenceHeartbeatOpened): 3}
	view.CadenceEvents = []heightsync.CadenceEvent{
		{Event: heightsync.CadenceHeartbeatOpened, TurnStart: 1, HRef: 10},
		{Event: heightsync.CadenceHeartbeatOpened, TurnStart: 2, HRef: 11},
		{Event: heightsync.CadenceHeartbeatOpened, TurnStart: 3, HRef: 12},
	}
	g := &Gateway{runtimeOrder: []*devshardRuntime{{id: "12", testHeightSyncView: &view}}}
	families := gatherHeightSync(t, g, false)
	requireMetricCounterValue(t, families, "devshard_gateway_heightsync_cadence_events_total", map[string]string{"devshard_id": "12", "event": "heartbeat_opened"}, 3)

	req := httptest.NewRequest(http.MethodGet, "/v1/debug/heightsync", nil)
	rec := httptest.NewRecorder()
	g.handleDebugHeightSync(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Escrows []struct {
			CadenceEvents []heightsync.CadenceEvent `json:"cadence_events"`
		} `json:"escrows"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Escrows, 1)
	require.Len(t, body.Escrows[0].CadenceEvents, 3)
	require.Equal(t, uint64(1), body.Escrows[0].CadenceEvents[0].TurnStart)
	require.Equal(t, uint64(3), body.Escrows[0].CadenceEvents[2].TurnStart)
}

func TestGatewayHeightSync_InferenceDischargeIsVisible(t *testing.T) {
	view := threeSlotView("12")
	view.CadenceCounts = map[string]uint64{
		string(heightsync.CadenceDischargedByInference): 4,
	}
	view.CadenceEvents = []heightsync.CadenceEvent{
		{Event: heightsync.CadenceDischargedByInference, HRef: 50},
	}
	g := &Gateway{runtimeOrder: []*devshardRuntime{{id: "12", testHeightSyncView: &view}}}
	families := gatherHeightSync(t, g, false)
	requireMetricCounterValue(t, families, "devshard_gateway_heightsync_cadence_events_total", map[string]string{"devshard_id": "12", "event": "discharged_by_inference"}, 4)
	requireNoMetricWithLabels(t, families, "devshard_gateway_heightsync_cadence_events_total", map[string]string{"devshard_id": "12", "event": "heartbeat_opened"})
}

func TestGatewayHeightSync_AbandonedTurnCounted(t *testing.T) {
	view := threeSlotView("12")
	view.AbandonedTurns = 2
	view.CadenceCounts = map[string]uint64{
		string(heightsync.CadenceTurnAbandoned):   2,
		string(heightsync.CadenceHeartbeatOpened): 3,
	}
	view.CadenceEvents = []heightsync.CadenceEvent{
		{Event: heightsync.CadenceTurnAbandoned, TurnStart: 1},
		{Event: heightsync.CadenceHeartbeatOpened, TurnStart: 2},
	}
	g := &Gateway{runtimeOrder: []*devshardRuntime{{id: "12", testHeightSyncView: &view}}}
	families := gatherHeightSync(t, g, false)
	requireMetricCounterValue(t, families, "devshard_gateway_heightsync_turns_abandoned_total", map[string]string{"devshard_id": "12"}, 2)
	requireMetricCounterValue(t, families, "devshard_gateway_heightsync_cadence_events_total", map[string]string{"devshard_id": "12", "event": "turn_abandoned"}, 2)
	requireMetricCounterValue(t, families, "devshard_gateway_heightsync_cadence_events_total", map[string]string{"devshard_id": "12", "event": "heartbeat_opened"}, 3)
}

func TestGatewayHeightSync_BucketSealsAfterDAck(t *testing.T) {
	view := threeSlotView("12")
	view.AnchorsLastSealed = &heightsync.SealedAnchorCounts{
		Height: 10,
		ByKind: map[string]int{heightsync.AnchorKindResponse: 3},
	}
	g := &Gateway{runtimeOrder: []*devshardRuntime{{id: "12", testHeightSyncView: &view}}}
	families := gatherHeightSync(t, g, false)
	requireMetricGaugeValue(t, families, "devshard_gateway_heightsync_anchors_last_block", map[string]string{"devshard_id": "12", "kind": "response"}, 3)
	requireMetricGaugeValue(t, families, "devshard_gateway_heightsync_anchors_last_block", map[string]string{"devshard_id": "12", "kind": "heartbeat"}, 0)
}

func TestGatewayHeightSync_BlockWithoutAnchorCounted(t *testing.T) {
	view := threeSlotView("12")
	view.BlocksWithoutAnchor = 1
	view.AnchorsLastSealed = &heightsync.SealedAnchorCounts{Height: 7, ByKind: map[string]int{}}
	g := &Gateway{runtimeOrder: []*devshardRuntime{{id: "12", testHeightSyncView: &view}}}
	families := gatherHeightSync(t, g, false)
	requireMetricCounterValue(t, families, "devshard_gateway_heightsync_blocks_without_anchor_total", map[string]string{"devshard_id": "12"}, 1)
}

func TestGatewayHeightSync_PeerSeenMatrix(t *testing.T) {
	view := heightsync.OperatorView{
		DevshardID: "12",
		Slots: []heightsync.SlotIdentity{
			{Slot: 0}, {Slot: 1}, {Slot: 2}, {Slot: 3},
		},
		PeerSeen: []heightsync.PeerSeenRow{
			{Observer: 0, Bits: []byte{0b00001011}, Count: 3}, // 0,1,3 — not 2
			{Observer: 1, Bits: []byte{0b00001111}, Count: 4},
			{Observer: 2, Bits: []byte{0b00001111}, Count: 4},
			{Observer: 3, Bits: []byte{0b00001111}, Count: 4},
		},
	}
	g := &Gateway{runtimeOrder: []*devshardRuntime{{id: "12", testHeightSyncView: &view}}}
	families := gatherHeightSync(t, g, true)
	requireMetricGaugeValue(t, families, "devshard_gateway_heightsync_peer_seen", map[string]string{"devshard_id": "12", "observer_slot": "0", "subject_slot": "2"}, 0)
	requireMetricGaugeValue(t, families, "devshard_gateway_heightsync_peer_seen", map[string]string{"devshard_id": "12", "observer_slot": "0", "subject_slot": "1"}, 1)
	requireMetricGaugeValue(t, families, "devshard_gateway_heightsync_peer_seen_unseen_total", map[string]string{"devshard_id": "12", "subject_slot": "2"}, 1)
	requireMetricGaugeValue(t, families, "devshard_gateway_heightsync_peer_seen_count", map[string]string{"devshard_id": "12", "observer_slot": "0"}, 3)
}

func TestGatewayHeightSync_SettleDropsEverySeries(t *testing.T) {
	view := threeSlotView("12")
	view.Tips = []heightsync.OriginTip{{Slot: 0, Originator: "host-a", Height: 100, Fresh: true}}
	view.CadenceCounts = map[string]uint64{"heartbeat_opened": 1}
	g := &Gateway{runtimeOrder: []*devshardRuntime{{id: "12", testHeightSyncView: &view}}}
	families := gatherHeightSync(t, g, false)
	require.True(t, anyMetricHasLabel(families, "devshard_id", "12"))

	g.runtimeOrder = nil
	families = gatherHeightSync(t, g, false)
	require.False(t, anyMetricHasLabel(families, "devshard_id", "12"), "settled escrow must drop every series labelled with its id")
}

func TestGatewayHeightSync_PeerMatrixOptIn(t *testing.T) {
	view := heightsync.OperatorView{
		DevshardID: "12",
		Slots:      []heightsync.SlotIdentity{{Slot: 0}, {Slot: 1}},
		PeerSeen: []heightsync.PeerSeenRow{
			{Observer: 0, Bits: []byte{0b00000011}, Count: 2},
			{Observer: 1, Bits: []byte{0b00000011}, Count: 2},
		},
	}
	g := &Gateway{runtimeOrder: []*devshardRuntime{{id: "12", testHeightSyncView: &view}}}
	families := gatherHeightSync(t, g, false)
	requireNoMetricFamily(t, families, "devshard_gateway_heightsync_peer_seen")
	requireMetricGaugeValue(t, families, "devshard_gateway_heightsync_peer_seen_count", map[string]string{"devshard_id": "12", "observer_slot": "0"}, 2)
	requireMetricGaugeValue(t, families, "devshard_gateway_heightsync_peer_seen_unseen_total", map[string]string{"devshard_id": "12", "subject_slot": "0"}, 0)

	req := httptest.NewRequest(http.MethodGet, "/v1/debug/heightsync", nil)
	rec := httptest.NewRecorder()
	g.handleDebugHeightSync(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "peer_seen")
}

func TestGatewayHeightSync_ArmingPredictionIsInert(t *testing.T) {
	view := threeSlotView("12")
	view.IdleTimeout = 12 * time.Second
	view.Contacts = []heightsync.SlotContact{
		{Slot: 0, LastAt: time.Unix(1, 0), SinceContact: time.Second},
		{Slot: 1, LastAt: time.Unix(1, 0), SinceContact: 13 * time.Second},
	}
	calls := 0
	g := &Gateway{
		runtimeOrder:     []*devshardRuntime{{id: "12", testHeightSyncView: &view}},
		heightSyncCloser: func(string, string) { calls++ },
	}
	families := gatherHeightSync(t, g, false)
	requireMetricGaugeValue(t, families, "devshard_gateway_heightsync_arming_predicted", map[string]string{"devshard_id": "12", "slot": "1"}, 1)
	requireMetricGaugeValue(t, families, "devshard_gateway_heightsync_arming_predicted", map[string]string{"devshard_id": "12", "slot": "0"}, 0)
	require.Zero(t, calls, "arming_predicted must not invoke closing or routing")
}

func TestGatewayHeightSync_UnwiredSessionEmitsNothing(t *testing.T) {
	g := &Gateway{runtimeOrder: []*devshardRuntime{
		{id: "off", session: nil},
		{id: "also-off", testHeightSyncView: nil},
	}}
	families := gatherHeightSync(t, g, false)
	requireNoMetricWithLabels(t, families, "devshard_gateway_heightsync_seconds_since_contact", map[string]string{"devshard_id": "off"})
	requireNoMetricWithLabels(t, families, "devshard_gateway_heightsync_seconds_since_contact", map[string]string{"devshard_id": "also-off"})
	requireNoMetricFamily(t, families, "devshard_gateway_heightsync_cadence_events_total")
}

func TestGatewayHeightSync_GatherDoesNotBlockOnSessionLock(t *testing.T) {
	// Live path: Collect reads CachedHeightSyncView only. Holding s.mu (as
	// persistDiffRetryLocked does) must not stall Gather; the previous view
	// still emits.
	numHosts := 3
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()
	clients := make([]user.HostClient, numHosts)
	for i := range hosts {
		sm := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, userKey.Address(), verifier)
		h, err := host.NewHost(sm, hosts[i], stub.NewInferenceEngine(), "escrow-1", group, nil, host.WithGrace(10))
		require.NoError(t, err)
		clients[i] = &user.InProcessClient{Host: h}
	}
	userSM := statetest.MustStateMachine(t, "escrow-1", config, group, 100000, userKey.Address(), verifier)
	session, err := user.NewSession(userSM, userKey, "escrow-1", group, clients, verifier,
		user.WithHeightSyncCadence(10, uint64(numHosts)))
	require.NoError(t, err)
	session.SetHeightSyncPeerTips(transport.NewHeightSyncPeerTips())
	require.True(t, session.HeightSyncWired())
	require.NotEmpty(t, session.CachedHeightSyncView().Slots)

	g := &Gateway{runtimeOrder: []*devshardRuntime{{id: "escrow-1", session: session}}}
	release := session.TestingOnlyHoldMu()
	defer release()

	done := make(chan []*dto.MetricFamily, 1)
	go func() { done <- gatherHeightSync(t, g, false) }()
	select {
	case families := <-done:
		require.True(t, anyMetricHasLabel(families, "devshard_id", "escrow-1"),
			"previous published view must still emit while s.mu is held")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Gather blocked on s.mu — height-sync Collect must use the cache only")
	}
}

func TestGatewayMetrics_NoHostLogPlaneTurnState(t *testing.T) {
	// Unlabelled host gauges stay on devshardd. Gateway Attach must not
	// register them, even when process-global SetTurnState has run.
	hostReg := prometheus.NewRegistry()
	require.NoError(t, heightsync.RegisterLogPlaneMetrics(hostReg))
	heightsync.SetTurnState("open")
	heightsync.SetTurnState("complete") // two sessions flapping must not reach gateway

	view := threeSlotView("a")
	view2 := threeSlotView("b")
	g := &Gateway{runtimeOrder: []*devshardRuntime{
		{id: "a", testHeightSyncView: &view},
		{id: "b", testHeightSyncView: &view2},
	}}
	m := NewDevshardMetrics()
	m.AttachGateway(g)
	families, err := m.registry.Gather()
	require.NoError(t, err)
	requireNoMetricFamily(t, families, heightsync.MetricTurnState)
	requireNoMetricFamily(t, families, heightsync.MetricCloseReadyArmed)
	requireNoMetricFamily(t, families, heightsync.MetricPeerSeenSlots)

	hostFamilies, err := hostReg.Gather()
	require.NoError(t, err)
	require.True(t, anyMetricHasLabel(hostFamilies, "state", "complete") ||
		metricFamilyPresent(hostFamilies, heightsync.MetricTurnState),
		"host registry still exposes turn_state")
}

func metricFamilyPresent(families []*dto.MetricFamily, name string) bool {
	for _, f := range families {
		if f.GetName() == name {
			return true
		}
	}
	return false
}

func requireNoMetricWithLabels(t *testing.T, families []*dto.MetricFamily, name string, labels map[string]string) {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metricLabelsMatch(metric, labels) {
				t.Fatalf("metric %s with labels %v should be absent", name, labels)
			}
		}
	}
}

func requireNoMetricFamily(t *testing.T, families []*dto.MetricFamily, name string) {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			t.Fatalf("metric family %s should be absent", name)
		}
	}
}

func anyMetricHasLabel(families []*dto.MetricFamily, key, value string) bool {
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == key && lp.GetValue() == value {
					return true
				}
			}
		}
	}
	return false
}
