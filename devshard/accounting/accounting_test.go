package accounting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"devshard/types"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

var accountingTestNow = time.Unix(1_700_000_000, 0).UTC()

func TestTrackerCountsAndCrossChecks(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordGhost("e1", 1, PhasePoC, QuarantineProbe, NoSendParticipantThrottled, ""))
	require.NoError(t, tr.RecordDiff("e1", 2, true))
	require.NoError(t, tr.RecordRealSend("e1", 2, accountingTestNow, PhaseNormal, QuarantineShadow))
	require.NoError(t, tr.RecordUsage("e1", 2, UsageWinner))
	require.NoError(t, tr.RecordProtocol("e1", 2, 0, ProtocolFinishApplied, types.HostStats{}))
	require.NoError(t, tr.RecordDiff("e1", 3, false))
	require.NoError(t, tr.RecordDiff("e1", 4, true))
	require.NoError(t, tr.RecordRealSend("e1", 4, accountingTestNow.Add(-2*time.Minute), PhaseNormal, QuarantineNone))
	require.NoError(t, tr.RecordTimeout(TimeoutRecord{
		EscrowID: "e1", Nonce: 4, Kind: TimeoutRefused, Phase: PhaseNormal, Outcome: TimeoutApplied,
	}))
	require.NoError(t, tr.RecordProtocol("e1", 4, 0, ProtocolTimeoutApplied, types.HostStats{Missed: 1}))

	records := tr.Query(QueryFilter{EpochIndex: 7})
	require.Len(t, records, 2)
	var dispositions = make(map[Disposition]uint64)
	var applied, missed, errors uint64
	for _, record := range records {
		for d, count := range record.Dispositions {
			dispositions[d] += count
		}
		applied += record.TimeoutOutcomes[TimeoutApplied]
		missed += record.ProtocolMisses
		errors += record.CrossChecks.ErrorCount
	}
	require.Equal(t, uint64(1), dispositions[DispositionGhost])
	require.Equal(t, uint64(1), dispositions[DispositionFinishedUsed])
	require.Equal(t, uint64(1), dispositions[DispositionProtocolOnly])
	require.Equal(t, missed, applied)
	require.Zero(t, errors)
}

func TestRestartTurnsLiveStateIntoUnclassified(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.db")
	tr, err := OpenTracker(path, 0, 0)
	require.NoError(t, err)
	tr.now = func() time.Time { return accountingTestNow }
	registerEscrow(t, tr, "e1", 8, "m")
	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordRealSend("e1", 1, accountingTestNow, PhaseNormal, QuarantineNone))
	require.Equal(t, uint64(1), onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 8}), "p1").InFlight)
	require.NoError(t, tr.Close())

	reopened, err := OpenTracker(path, 0, 0)
	require.NoError(t, err)
	defer reopened.Close()
	record := onlyRecord(t, reopened.Query(QueryFilter{EpochIndex: 8}), "p1")
	require.Zero(t, record.InFlight)
	require.Equal(t, uint64(1), record.Unclassified)
}

func TestHTTPFiltersAndMetrics(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 9, "m1")
	registerEscrow(t, tr, "e2", 9, "m2")
	require.NoError(t, tr.RecordDiff("e1", 1, false))
	require.NoError(t, tr.RecordDiff("e2", 1, false))
	require.Error(t, tr.RecordGhost("missing", 1, PhaseNormal, QuarantineNone, NoSendUnknown, ""))
	handler := NewHandler(tr, func(context.Context) (uint64, error) { return 9, nil })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/epochs/current/participants?model=m1&escrow_id=e1,e2", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		RecordingErrors uint64              `json:"recording_errors"`
		WriterErrors    uint64              `json:"writer_errors"`
		Participants    []ParticipantRecord `json:"participants"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, uint64(1), body.RecordingErrors)
	require.Zero(t, body.WriterErrors)
	require.Len(t, body.Participants, 2)
	for _, participant := range body.Participants {
		require.Equal(t, "m1", participant.Model)
		require.Equal(t, "e1", participant.LatestNonces[0].EscrowID)
	}

	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(NewCollector(tr, func(context.Context) (uint64, error) { return 9, nil }))
	families, err := registry.Gather()
	require.NoError(t, err)
	require.NotEmpty(t, families)
	record := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 9, Model: "m1"}), "p1")
	labels := map[string]string{"participant": "p1", "model": "m1"}
	for metricName, expected := range map[string]uint64{
		"devshard_accounting_assigned_nonces":        record.AssignedNonces,
		"devshard_accounting_protocol_misses":        record.ProtocolMisses,
		"devshard_accounting_protocol_invalid":       record.ProtocolInvalid,
		"devshard_accounting_in_flight":              record.InFlight,
		"devshard_accounting_timeout_pending":        record.TimeoutPending,
		"devshard_accounting_pending_classification": record.PendingClassification,
		"devshard_accounting_unclassified":           record.Unclassified,
		"devshard_accounting_overclassified":         record.Overclassified,
		"devshard_accounting_unknown_reason_total":   record.UnknownReasonTotal,
		"devshard_accounting_cross_check_error":      record.CrossChecks.ErrorCount,
	} {
		require.Equal(t, float64(expected), metricGaugeValue(t, families, metricName, labels), metricName)
	}
	dispositionLabels := map[string]string{
		"participant": "p1",
		"model":       "m1",
		"disposition": string(DispositionProtocolOnly),
	}
	require.Equal(t, float64(1), metricGaugeValue(
		t,
		families,
		"devshard_accounting_disposition",
		dispositionLabels,
	))
	recordingErrors, writerErrors := tr.ErrorCounts()
	require.Equal(t, float64(recordingErrors), metricGaugeValue(
		t,
		families,
		"devshard_accounting_recording_errors",
		nil,
	))
	require.Equal(t, float64(writerErrors), metricGaugeValue(
		t,
		families,
		"devshard_accounting_writer_errors",
		nil,
	))
	require.Equal(t, 1, metricSeriesCount(t, families, "devshard_accounting_recording_errors"))
	require.Equal(t, 1, metricSeriesCount(t, families, "devshard_accounting_writer_errors"))
}

func TestNonExecutionCreditIdentity(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 10, "m")
	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordGhost("e1", 1, PhaseNormal, QuarantineNone, NoSendPoCUnavailable, ""))
	require.NoError(t, tr.RecordDiff("e1", 2, false))
	require.NoError(t, tr.RecordDiff("e1", 3, true))
	require.NoError(t, tr.RecordRealSend("e1", 3, accountingTestNow.Add(-2*time.Minute), PhaseNormal, QuarantineNone))
	require.NoError(t, tr.RecordTimeout(TimeoutRecord{
		EscrowID: "e1", Nonce: 3, Kind: TimeoutRefused, Phase: PhaseNormal,
		Outcome: TimeoutVoteCollectionFailed, FailureOrigin: FailureTransportUnknown,
	}))

	var assigned, executed, protocolMisses, nonExecution uint64
	for _, record := range tr.Query(QueryFilter{EpochIndex: 10}) {
		assigned += record.AssignedNonces
		executed += record.Dispositions[DispositionFinishedUsed] + record.Dispositions[DispositionFinishedUnused] + record.Dispositions[DispositionFinishedUsageUnknown]
		protocolMisses += record.ProtocolMisses
		nonExecution += record.Dispositions[DispositionProtocolOnly] + record.Dispositions[DispositionGhost] + record.Dispositions[DispositionUnfinishedRefused] + record.Unclassified
	}
	require.Equal(t, assigned-protocolMisses-executed, nonExecution)
}

func TestDeadlineDerivedDisposition(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 11, "m")
	now := accountingTestNow
	tr.now = func() time.Time { return now }

	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordRealSend("e1", 1, now, PhaseNormal, QuarantineNone))

	record := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 11}), "p1")
	require.Equal(t, uint64(1), record.InFlight)
	require.Zero(t, record.TimeoutPending)

	now = now.Add(66 * time.Second)
	record = onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 11}), "p1")
	require.Zero(t, record.InFlight)
	require.Equal(t, uint64(1), record.TimeoutPending)
	require.Equal(t, uint64(1), record.Dispositions[DispositionUnfinishedRefused])
	require.Equal(t, TimeoutOutcome(""), record.Counters[0].Key.TimeoutOutcome)

	confirmedAt := now.Unix()
	require.NoError(t, tr.RecordReceipt("e1", 1, confirmedAt))
	record = onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 11}), "p1")
	require.Equal(t, uint64(1), record.InFlight)
	require.Zero(t, record.Dispositions[DispositionUnfinishedRefused])

	now = time.Unix(confirmedAt, 0).Add(1206 * time.Second)
	record = onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 11}), "p1")
	require.Zero(t, record.InFlight)
	require.Equal(t, uint64(1), record.TimeoutPending)
	require.Equal(t, uint64(1), record.Dispositions[DispositionUnfinishedExecution])
}

func TestPreDeadlineSkipRemainsInFlight(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 21, "m")
	now := accountingTestNow
	tr.now = func() time.Time { return now }

	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordRealSend("e1", 1, now, PhaseNormal, QuarantineNone))
	require.NoError(t, tr.RecordTimeout(TimeoutRecord{
		EscrowID: "e1", Nonce: 1, Kind: TimeoutRefused, Phase: PhaseNormal,
		Outcome: TimeoutSkipped, Reason: TimeoutPhaseTransitionAborted,
	}))

	record := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 21}), "p1")
	require.Equal(t, uint64(1), record.InFlight)
	require.Zero(t, record.TimeoutOutcomes[TimeoutSkipped])

	now = now.Add(66 * time.Second)
	record = onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 21}), "p1")
	require.Zero(t, record.InFlight)
	require.Equal(t, uint64(1), record.Dispositions[DispositionUnfinishedRefused])
	require.Equal(t, uint64(1), record.TimeoutOutcomes[TimeoutSkipped])
}

func TestTimeoutAndFinishOrdering(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 12, "m")
	now := accountingTestNow
	tr.now = func() time.Time { return now }

	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordRealSend("e1", 1, now.Add(-2*time.Minute), PhaseNormal, QuarantineNone))
	require.NoError(t, tr.RecordTimeout(TimeoutRecord{
		EscrowID: "e1", Nonce: 1, Kind: TimeoutRefused, Phase: PhaseNormal,
		Outcome: TimeoutInsufficientVotes, FailureOrigin: FailureTransportUnknown,
	}))
	record := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 12}), "p1")
	require.Equal(t, uint64(1), record.Dispositions[DispositionUnfinishedRefused])

	require.NoError(t, tr.RecordReceipt("e1", 1, now.Unix()))
	record = onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 12}), "p1")
	require.Equal(t, uint64(1), record.InFlight)
	require.Zero(t, record.Dispositions[DispositionUnfinishedRefused])

	require.NoError(t, tr.RecordUsage("e1", 1, UsageWinner))
	require.NoError(t, tr.RecordProtocol("e1", 1, 1, ProtocolFinishApplied, types.HostStats{}))
	record = onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 12}), "p1")
	require.Equal(t, uint64(1), record.Dispositions[DispositionFinishedUsed])
	require.Zero(t, record.TimeoutOutcomes[TimeoutInsufficientVotes])

	require.NoError(t, tr.RecordDiff("e1", 2, true))
	require.NoError(t, tr.RecordRealSend("e1", 2, now.Add(-2*time.Minute), PhaseNormal, QuarantineNone))
	require.NoError(t, tr.RecordProtocol("e1", 2, 0, ProtocolTimeoutApplied, types.HostStats{Missed: 1}))
	require.Contains(t, tr.escrows["e1"].Live, uint64(2))
	require.NoError(t, tr.RecordTimeout(TimeoutRecord{
		EscrowID: "e1", Nonce: 2, Kind: TimeoutRefused, Phase: PhaseNormal, Outcome: TimeoutApplied,
	}))
	require.NotContains(t, tr.escrows["e1"].Live, uint64(2))
	record = onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 12}), "p0")
	require.Equal(t, uint64(1), record.TimeoutOutcomes[TimeoutApplied])
	require.Zero(t, record.CrossChecks.ErrorCount)
}

func TestCommittedDiffIsIdempotent(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 13, "m")
	diff := types.Diff{Nonce: 1}
	require.NoError(t, tr.RecordCommittedDiff("e1", diff, nil))
	require.NoError(t, tr.RecordCommittedDiff("e1", diff, nil))

	record := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 13}), "p1")
	require.Equal(t, uint64(1), record.Dispositions[DispositionProtocolOnly])
	require.Zero(t, record.Overclassified)
}

func TestRecordCommittedStateAlignsProtocolTotals(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 18, "m")
	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordRealSend(
		"e1",
		1,
		accountingTestNow.Add(-2*time.Minute),
		PhaseNormal,
		QuarantineNone,
	))
	require.NoError(t, tr.RecordTimeout(TimeoutRecord{
		EscrowID: "e1",
		Nonce:    1,
		Kind:     TimeoutRefused,
		Phase:    PhaseNormal,
		Outcome:  TimeoutApplied,
	}))
	diff := types.Diff{
		Nonce: 2,
		Txs: []*types.DevshardTx{{
			Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{
				InferenceId: 1,
			}},
		}},
	}
	snapshot := types.EscrowState{
		LatestNonce: 2,
		Phase:       types.PhaseActive,
		HostStats: map[uint32]*types.HostStats{
			1: {Missed: 1},
		},
	}
	require.NoError(t, tr.RecordCommittedState("e1", diff, nil, snapshot))

	record := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 18}), "p1")
	require.Equal(t, uint64(1), record.ProtocolMisses)
	require.Equal(t, uint64(1), record.TimeoutOutcomes[TimeoutApplied])
	require.Zero(t, record.CrossChecks.ErrorCount)
}

func TestRecordCommittedStateReplaySynchronizesHostStatsWithoutInventingTimeouts(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 19, "m")
	diff := types.Diff{Nonce: 1}
	snapshot := types.EscrowState{
		LatestNonce: 1,
		Phase:       types.PhaseActive,
		HostStats: map[uint32]*types.HostStats{
			1: {Missed: 1},
		},
	}
	require.NoError(t, tr.RecordCommittedState("e1", diff, nil, snapshot))
	snapshot.HostStats[1].Missed = 2
	require.NoError(t, tr.RecordCommittedState("e1", diff, nil, snapshot))

	record := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 19}), "p1")
	require.Equal(t, uint64(1), record.Dispositions[DispositionProtocolOnly])
	require.Equal(t, uint64(2), record.ProtocolMisses)
	require.Zero(t, record.TimeoutOutcomes[TimeoutApplied])
	require.Equal(t, uint64(2), record.CrossChecks.ErrorCount)
}

func TestRecordCommittedStateValidatesBeforeMutation(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 20, "m")
	require.NoError(t, tr.RecordDiff("e1", 1, false))
	beforeLatest := tr.escrows["e1"].Latest
	beforeCounters := len(tr.escrows["e1"].Counters)

	err := tr.RecordCommittedState(
		"e1",
		types.Diff{Nonce: 2},
		[]VerdictRecord{{Nonce: 1, Slot: 99, Kind: ProtocolInvalidated}},
		types.EscrowState{LatestNonce: 2, Phase: types.PhaseActive},
	)
	require.ErrorContains(t, err, "slot 99 out of range")
	require.Equal(t, beforeLatest, tr.escrows["e1"].Latest)
	require.Len(t, tr.escrows["e1"].Counters, beforeCounters)
}

func TestGatewayPolicyFactsCoverAllBranches(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 14, "m")
	recorder := NewRecorder(tr, func() string { return "poc" })
	reasons := []string{
		string(NoSendParticipantThrottled),
		string(NoSendParticipantCapability),
		string(NoSendPoCUnavailable),
		string(NoSendNoCompatibleAfterStale),
		"new_policy",
	}
	for i, reason := range reasons {
		nonce := uint64(i + 1)
		require.NoError(t, tr.RecordDiff("e1", nonce, true))
		quarantine := ""
		if nonce == 1 {
			quarantine = "probe"
		}
		recorder.Ghost("e1", nonce, reason, quarantine)
	}

	for i, quarantine := range []string{"shadow", "probation", ""} {
		nonce := uint64(i + 6)
		require.NoError(t, tr.RecordDiff("e1", nonce, true))
		recorder.RealSend("e1", nonce, accountingTestNow, quarantine)
	}
	recorder.Usage("e1", 6, 6)
	recorder.Usage("e1", 7, 6)
	recorder.Usage("e1", 8, 0)
	require.NoError(t, tr.RecordProtocol("e1", 6, 0, ProtocolFinishApplied, types.HostStats{}))
	require.NoError(t, tr.RecordProtocol("e1", 7, 1, ProtocolFinishApplied, types.HostStats{}))
	require.NoError(t, tr.RecordProtocol("e1", 8, 0, ProtocolFinishApplied, types.HostStats{}))

	reasonCounts := make(map[NoSendReason]uint64)
	quarantineCounts := make(map[QuarantineMode]uint64)
	dispositions := make(map[Disposition]uint64)
	var unknown uint64
	for _, participant := range tr.Query(QueryFilter{EpochIndex: 14}) {
		for disposition, count := range participant.Dispositions {
			dispositions[disposition] += count
		}
		unknown += participant.UnknownReasonTotal
		for _, counter := range participant.Counters {
			reasonCounts[counter.Key.NoSendReason] += counter.Count
			quarantineCounts[counter.Key.QuarantineMode] += counter.Count
		}
	}
	for _, reason := range []NoSendReason{
		NoSendParticipantThrottled,
		NoSendParticipantCapability,
		NoSendPoCUnavailable,
		NoSendNoCompatibleAfterStale,
		NoSendUnknown,
	} {
		require.Equal(t, uint64(1), reasonCounts[reason])
	}
	require.Equal(t, uint64(1), quarantineCounts[QuarantineProbe])
	require.Equal(t, uint64(1), quarantineCounts[QuarantineShadow])
	require.Equal(t, uint64(1), quarantineCounts[QuarantineProbation])
	require.Equal(t, uint64(1), dispositions[DispositionFinishedUsed])
	require.Equal(t, uint64(1), dispositions[DispositionFinishedUnused])
	require.Equal(t, uint64(1), dispositions[DispositionFinishedUsageUnknown])
	require.Equal(t, uint64(1), unknown)
}

func TestAllTimeoutOutcomesRemainVisible(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 15, "m")
	outcomes := []TimeoutOutcome{
		TimeoutSkipped,
		TimeoutVoteCollectionFailed,
		TimeoutInsufficientVotes,
		TimeoutDiffSendFailed,
		TimeoutApplied,
	}
	for i, outcome := range outcomes {
		nonce := uint64(i + 1)
		require.NoError(t, tr.RecordDiff("e1", nonce, true))
		require.NoError(t, tr.RecordRealSend("e1", nonce, accountingTestNow.Add(-time.Hour), PhaseNormal, QuarantineNone))
		kind := TimeoutRefused
		if nonce%2 == 0 {
			kind = TimeoutExecution
			require.NoError(t, tr.RecordReceipt("e1", nonce, accountingTestNow.Add(-time.Hour).Unix()))
		}
		require.NoError(t, tr.RecordTimeout(TimeoutRecord{
			EscrowID: "e1", Nonce: nonce, Kind: kind, Phase: PhaseNormal, Outcome: outcome,
		}))
		if outcome == TimeoutApplied {
			slot := AssignedNonceSlot(nonce, 2)
			require.NoError(t, tr.RecordProtocol("e1", nonce, slot, ProtocolTimeoutApplied, types.HostStats{Missed: 1}))
		}
	}

	counts := make(map[TimeoutOutcome]uint64)
	var refused, execution, unknown uint64
	for _, record := range tr.Query(QueryFilter{EpochIndex: 15}) {
		for outcome, count := range record.TimeoutOutcomes {
			counts[outcome] += count
		}
		refused += record.Dispositions[DispositionUnfinishedRefused]
		execution += record.Dispositions[DispositionUnfinishedExecution]
		unknown += record.UnknownReasonTotal
		for _, counter := range record.Counters {
			if counter.Key.TimeoutOutcome != "" && counter.Key.TimeoutOutcome != TimeoutApplied {
				require.NotEmpty(t, counter.Key.TimeoutReason)
			}
		}
	}
	for _, outcome := range outcomes {
		require.Equal(t, uint64(1), counts[outcome])
	}
	require.Equal(t, uint64(3), refused)
	require.Equal(t, uint64(2), execution)
	require.Equal(t, uint64(4), unknown)
}

func TestUnknownTimeoutResultStaysPendingAndRecordsError(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 17, "m")
	recorder := NewRecorder(tr, nil)
	for nonce := uint64(1); nonce <= 2; nonce++ {
		require.NoError(t, tr.RecordDiff("e1", nonce, true))
		require.NoError(t, tr.RecordRealSend(
			"e1",
			nonce,
			accountingTestNow.Add(-time.Hour),
			PhaseNormal,
			QuarantineNone,
		))
	}

	recorder.TimeoutResult("e1", 1, "refused", "unexpected_action", "mystery", "", "")
	recorder.TimeoutResult("e1", 2, "refused", "failed", "mystery", "", "")

	var pending, voteCollectionFailed uint64
	for _, record := range tr.Query(QueryFilter{EpochIndex: 17}) {
		pending += record.TimeoutPending
		voteCollectionFailed += record.TimeoutOutcomes[TimeoutVoteCollectionFailed]
	}
	require.Equal(t, uint64(2), pending)
	require.Zero(t, voteCollectionFailed)
	recordingErrors, _ := tr.ErrorCounts()
	require.Equal(t, uint64(2), recordingErrors)
}

func TestNilRecorderMethodsAreNoOps(t *testing.T) {
	var recorder *Recorder
	require.NotPanics(t, func() {
		recorder.Ghost("e1", 1, "", "")
		recorder.RealSend("e1", 1, time.Now(), "")
		recorder.Usage("e1", 1, 1)
		recorder.TimeoutResult("e1", 1, "", "", "", "", "")
		recorder.Finalize("e1")
		recorder.Settled("e1")
		recorder.Flush()
		require.NoError(t, recorder.Close())
		require.Nil(t, recorder.Tracker())
		recorder.DiffObserver("e1", nil)(types.Diff{})
	})
}

func TestRestartMissGapDoesNotInventDisposition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounting.db")
	tr, err := OpenTracker(path, 0, time.Hour)
	require.NoError(t, err)
	tr.now = func() time.Time { return accountingTestNow }
	registerEscrow(t, tr, "e1", 16, "m")
	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordRealSend("e1", 1, accountingTestNow, PhaseNormal, QuarantineNone))
	require.NoError(t, tr.Close())

	reopened, err := OpenTracker(path, 0, time.Hour)
	require.NoError(t, err)
	defer reopened.Close()
	reopened.now = func() time.Time { return accountingTestNow }
	require.NoError(t, reopened.SyncState("e1", 1, map[uint32]*types.HostStats{
		1: {Missed: 1},
	}))

	record := onlyRecord(t, reopened.Query(QueryFilter{EpochIndex: 16}), "p1")
	require.Equal(t, uint64(1), record.ProtocolMisses)
	require.Equal(t, uint64(1), record.Unclassified)
	require.Zero(t, record.Overclassified)
	require.Zero(t, record.Dispositions[DispositionUnfinishedRefused])
	require.Zero(t, record.TimeoutOutcomes[TimeoutApplied])
	require.Equal(t, uint64(1), record.CrossChecks.ErrorCount)
}

func TestFinalizedEscrowKeepsLiveClassification(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 22, "m")
	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordRealSend("e1", 1, accountingTestNow, PhaseNormal, QuarantineNone))

	for _, phase := range []EscrowPhase{EscrowFinalized, EscrowSettled} {
		require.NoError(t, tr.RecordPhase("e1", phase))
		record := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 22}), "p1")
		require.Equal(t, uint64(1), record.InFlight)
		require.Zero(t, record.Unclassified)
	}
}

func newTestTracker(t *testing.T) *Tracker {
	t.Helper()
	tr, err := OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, 0)
	require.NoError(t, err)
	tr.now = func() time.Time { return accountingTestNow }
	t.Cleanup(func() { require.NoError(t, tr.Close()) })
	return tr
}

func registerEscrow(t *testing.T, tr *Tracker, id string, epoch uint64, model string) {
	t.Helper()
	require.NoError(t, tr.RegisterEscrow(EscrowMetadata{
		EscrowID:             id,
		CreationEpoch:        epoch,
		Model:                model,
		Phase:                EscrowActive,
		RefusalTimeout:       60,
		ExecutionTimeout:     1200,
		TimeoutBufferSeconds: 5,
		Slots: []types.SlotAssignment{
			{SlotID: 0, ValidatorAddress: "p0"},
			{SlotID: 1, ValidatorAddress: "p1"},
		},
	}))
}

func onlyRecord(t *testing.T, records []ParticipantRecord, participant string) ParticipantRecord {
	t.Helper()
	for _, record := range records {
		if record.Participant == participant {
			return record
		}
	}
	t.Fatalf("missing participant %s", participant)
	return ParticipantRecord{}
}

func metricGaugeValue(
	t *testing.T,
	families []*dto.MetricFamily,
	name string,
	labels map[string]string,
) float64 {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			matched := true
			for labelName, labelValue := range labels {
				found := false
				for _, pair := range metric.Label {
					if pair.GetName() == labelName && pair.GetValue() == labelValue {
						found = true
						break
					}
				}
				if !found {
					matched = false
					break
				}
			}
			if matched && metric.Gauge != nil {
				return metric.Gauge.GetValue()
			}
		}
	}
	t.Fatalf("missing metric %s with labels %v", name, labels)
	return 0
}

func metricSeriesCount(t *testing.T, families []*dto.MetricFamily, name string) int {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			return len(family.Metric)
		}
	}
	t.Fatalf("missing metric %s", name)
	return 0
}
