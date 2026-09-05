package heightsync

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestIncMarks_UnknownMapsToUnknown(t *testing.T) {
	require.Equal(t, "dispute_originator", markKindLabel(string(MarkDisputeOriginator)))
	require.Equal(t, "height_unbacked", markKindLabel(string(MarkHeightUnbacked)))
	require.Equal(t, "unknown", markKindLabel("not-a-kind"))
	require.Equal(t, "unknown", markKindLabel(""))

	require.NoError(t, RegisterLogPlaneMetrics(prometheus.NewRegistry()))
	logPlanePromMu.RLock()
	c := marksCounter
	logPlanePromMu.RUnlock()
	require.NotNil(t, c)

	before := counterVecValue(t, c, "unknown")
	IncMarks("not-a-kind")
	require.Equal(t, before+1, counterVecValue(t, c, "unknown"))
}

func TestMarksCountedOnRetentionNotEvaluation(t *testing.T) {
	require.NoError(t, RegisterLogPlaneMetrics(prometheus.NewRegistry()))
	logPlanePromMu.RLock()
	marks, stale := marksCounter, staleStampCounter
	logPlanePromMu.RUnlock()
	require.NotNil(t, marks)

	beforeVector := counterVecValue(t, marks, "vector_contradiction")
	beforeAdmission := counterVecValue(t, marks, "l5a_admission")
	beforeStale := counterVecValue(t, stale, "l5a_admission")

	// The compose trial loop re-checks a growing prefix once per tx, so the
	// same mark is produced many times for one diff.
	var res LogPlaneResult
	for i := 0; i < 5; i++ {
		res.mark(AttributableMark{Kind: MarkVectorContradiction})
		res.mark(AttributableMark{Kind: MarkAdmissionDelta})
	}
	require.Equal(t, beforeVector, counterVecValue(t, marks, "vector_contradiction"),
		"evaluating a diff must not move marks_total")
	require.Equal(t, beforeStale, counterVecValue(t, stale, "l5a_admission"),
		"evaluating a diff must not move stale_stamp_total")

	log := NewMarkLog()
	log.Append(res.Marks[0])
	log.Append(res.Marks[1])
	require.Equal(t, beforeVector+1, counterVecValue(t, marks, "vector_contradiction"),
		"retaining a mark is the one countable event")
	require.Equal(t, beforeAdmission+1, counterVecValue(t, marks, "l5a_admission"))
	require.Equal(t, beforeStale+1, counterVecValue(t, stale, "l5a_admission"))
}

func TestObserveLogPlaneReject_CountsOnlyActedOnVerdicts(t *testing.T) {
	require.NoError(t, RegisterLogPlaneMetrics(prometheus.NewRegistry()))
	logPlanePromMu.RLock()
	c := ackRejectedCounter
	logPlanePromMu.RUnlock()
	require.NotNil(t, c)

	before := counterVecValue(t, c, "ack_sig_invalid")
	var res LogPlaneResult
	for i := 0; i < 3; i++ {
		res = res.invalid(ErrAckSigInvalid, "ack_sig_invalid")
	}
	require.Equal(t, before, counterVecValue(t, c, "ack_sig_invalid"),
		"reaching a verdict is not the same as acting on it")

	ObserveLogPlaneReject(res.Reason)
	require.Equal(t, before+1, counterVecValue(t, c, "ack_sig_invalid"))

	ObserveLogPlaneReject("strong_required")
	require.Equal(t, before+1, counterVecValue(t, c, "ack_sig_invalid"),
		"only ack/heartbeat rejection reasons belong to this counter")
}

func TestHeartbeatReasonLabel_Allowlist(t *testing.T) {
	require.Equal(t, "quiet_session", heartbeatReasonLabel(string(ReasonQuietSession)))
	require.Equal(t, "turn_timeout", heartbeatReasonLabel(string(ReasonTurnTimeout)))
	require.Equal(t, "unknown", heartbeatReasonLabel(""))
	require.Equal(t, "unknown", heartbeatReasonLabel("wire-junk"))
}

func TestIncHeartbeatTurn_UsesRecordReason(t *testing.T) {
	reg := prometheus.NewRegistry()
	require.NoError(t, RegisterLogPlaneMetrics(reg))

	tr := NewTurnTracker(2, 1, DefaultHeartbeatConfig())
	tr.Observe(1, []*types.DevshardTx{{
		Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			SlotsNum:       2,
			ObservedHeight: 10,
			Reason:         string(ReasonQuietSession),
		}},
	}}, 10)
	tr.Observe(2, []*types.DevshardTx{{
		Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{
			RefNonce:       1,
			SlotId:         0,
			ObservedHeight: 10,
			SyncState:      types.SyncState_SYNCED,
		}},
	}}, 10)
	// Advance past ack deadline so quorum of 1 completes.
	tr.AdvanceHeight(10)

	rec := tr.Record(1)
	require.NotNil(t, rec)
	require.Equal(t, TurnComplete, rec.State)
	require.Equal(t, string(ReasonQuietSession), rec.Reason)

	logPlanePromMu.RLock()
	c := heartbeatTurnsCounter
	logPlanePromMu.RUnlock()
	require.Greater(t, counterVecValueLabels(t, c, "quiet_session", "complete"), 0.0)
}

func counterVecValueLabels(t *testing.T, c *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	m, err := c.GetMetricWithLabelValues(labels...)
	require.NoError(t, err)
	var pm dto.Metric
	require.NoError(t, m.Write(&pm))
	return pm.GetCounter().GetValue()
}

func TestPeerSeenByteLenValid(t *testing.T) {
	require.True(t, PeerSeenByteLenValid(nil, 0))
	require.False(t, PeerSeenByteLenValid([]byte{0}, 0))
	require.True(t, PeerSeenByteLenValid([]byte{0x07}, 3))
	require.True(t, PeerSeenByteLenValid([]byte{0}, 1))
	require.False(t, PeerSeenByteLenValid(nil, 3))
	require.False(t, PeerSeenByteLenValid(make([]byte, 2), 3))
	require.False(t, PeerSeenByteLenValid(make([]byte, 1024), 3))
}

func TestRepairProbeLabel_UnknownMapsToUnknown(t *testing.T) {
	require.Equal(t, "height", repairProbeLabel(RepairOutcomeHeight))
	require.Equal(t, "height", repairProbeLabel("height"))
	require.Equal(t, "unreachable", repairProbeLabel(RepairOutcomeUnreachable))
	require.Equal(t, "skipped_ack_landed", repairProbeLabel(string(RepairSkipAckLanded)))
	require.Equal(t, "budget_exhausted", repairProbeLabel(string(RepairSkipBudget)))
	require.Equal(t, "unknown", repairProbeLabel("not-a-label"))
	require.Equal(t, "unknown", repairProbeLabel(""))
	require.Equal(t, "unknown", repairProbeLabel(string(RepairSkipArmed)))
}

func TestIncRepairProbe_UnknownGathersAsUnknown(t *testing.T) {
	require.NoError(t, RegisterLogPlaneMetrics(prometheus.NewRegistry()))
	logPlanePromMu.RLock()
	c := repairProbesCounter
	logPlanePromMu.RUnlock()
	require.NotNil(t, c, "RegisterLogPlaneMetrics must install the process-global vec")

	before := counterVecValue(t, c, "unknown")
	IncRepairProbe("not-a-label")
	require.Equal(t, before+1, counterVecValue(t, c, "unknown"))
}

func TestRegisterLogPlaneMetrics_RegistersOnEveryRegisterer(t *testing.T) {
	regA := prometheus.NewRegistry()
	regB := prometheus.NewRegistry()
	require.NoError(t, RegisterLogPlaneMetrics(regA))
	require.NoError(t, RegisterLogPlaneMetrics(regB), "second registry must get the same collectors")

	SetTurnState("open")
	for _, reg := range []*prometheus.Registry{regA, regB} {
		families, err := reg.Gather()
		require.NoError(t, err)
		require.True(t, metricFamilyPresent(families, MetricTurnState),
			"both registries must expose %s", MetricTurnState)
	}
}

func TestRegisterAnchorMetrics_LeavesLogPlaneToTheVerifier(t *testing.T) {
	// Make the process-global log-plane instruments live, so absence below is
	// about this registry rather than about nothing having registered yet.
	require.NoError(t, RegisterLogPlaneMetrics(prometheus.NewRegistry()))
	SetTurnState("open")

	reg := prometheus.NewRegistry()
	require.NoError(t, RegisterAnchorMetrics(reg))
	IncOutboundAnchor("response", "escrow-1", "host-1")

	families, err := reg.Gather()
	require.NoError(t, err)
	require.True(t, metricFamilyPresent(families, MetricOutboundAnchorsTotal),
		"anchors are emitted by hosts and by the courier gateway")
	require.False(t, metricFamilyPresent(families, MetricTurnState),
		"a process that only emits anchors must not publish verifier log-plane series")
}

func metricFamilyPresent(families []*dto.MetricFamily, name string) bool {
	for _, f := range families {
		if f.GetName() == name {
			return true
		}
	}
	return false
}

func counterVecValue(t *testing.T, c *prometheus.CounterVec, outcome string) float64 {
	t.Helper()
	m, err := c.GetMetricWithLabelValues(outcome)
	require.NoError(t, err)
	var pm dto.Metric
	require.NoError(t, m.Write(&pm))
	return pm.GetCounter().GetValue()
}
