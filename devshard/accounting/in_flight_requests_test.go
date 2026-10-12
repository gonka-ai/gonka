package accounting

import (
	"testing"
	"time"

	"devshard/types"

	"github.com/stretchr/testify/require"
)

func recordAttempt(t *testing.T, tr *Tracker, escrowID string, nonce uint64, requestID string) {
	t.Helper()
	require.NoError(t, tr.RecordDiff(escrowID, nonce, true))
	require.NoError(t, tr.RecordRealSend(escrowID, nonce, accountingTestNow, PhaseNormal, QuarantineNone))
	require.NoError(t, tr.RecordRequestID(escrowID, nonce, requestID))
}

func TestOpenRequestSurvivesOnlyWhileTheClientIsUnanswered(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 15, "m")
	require.NoError(t, tr.RecordRequestStarted("e1", "req-1"))
	recordAttempt(t, tr, "e1", 1, "req-1")

	serving := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 15}), "p1")
	require.Equal(t, uint64(1), serving.InFlight)
	require.Equal(t, uint64(1), serving.InFlightRequests)

	require.NoError(t, tr.RecordRequestFinished("e1", "req-1"))

	answered := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 15}), "p1")
	require.Equal(t, uint64(1), answered.InFlight, "the losing nonce still hangs")
	require.Zero(t, answered.InFlightRequests, "but the client already has its answer")
}

func TestOpenRequestCountsOnEveryHostRacingIt(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 15, "m")
	require.NoError(t, tr.RecordRequestStarted("e1", "req-1"))
	recordAttempt(t, tr, "e1", 1, "req-1")
	recordAttempt(t, tr, "e1", 2, "req-1")

	records := tr.Query(QueryFilter{EpochIndex: 15})
	require.Equal(t, uint64(1), onlyRecord(t, records, "p1").InFlightRequests)
	require.Equal(t, uint64(1), onlyRecord(t, records, "p0").InFlightRequests)
}

func TestOneHostCountsAConcurrentRequestOnce(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 15, "m")
	require.NoError(t, tr.RecordRequestStarted("e1", "req-1"))
	recordAttempt(t, tr, "e1", 1, "req-1")
	recordAttempt(t, tr, "e1", 3, "req-1")

	serving := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 15}), "p1")
	require.Equal(t, uint64(2), serving.InFlight, "two nonces")
	require.Equal(t, uint64(1), serving.InFlightRequests, "one request")
}

func TestAFinishedNonceStopsHoldingItsRequest(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 15, "m")
	require.NoError(t, tr.RecordRequestStarted("e1", "req-1"))
	recordAttempt(t, tr, "e1", 1, "req-1")
	require.NoError(t, tr.RecordProtocol("e1", 1, 1, ProtocolFinishApplied, types.HostStats{}))

	finished := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 15}), "p1")
	require.Zero(t, finished.InFlightRequests, "a host that already answered is working on nothing")
}

func TestAnUnstartedRequestIsNeverCounted(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 15, "m")
	recordAttempt(t, tr, "e1", 1, "req-1")

	serving := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 15}), "p1")
	require.Equal(t, uint64(1), serving.InFlight)
	require.Zero(t, serving.InFlightRequests, "a nonce whose request the ledger never opened counts for nothing")
}

func TestEpochSummaryCountsDistinctOpenRequests(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 15, "m")
	require.NoError(t, tr.RecordRequestStarted("e1", "req-1"))
	require.NoError(t, tr.RecordRequestStarted("e1", "req-2"))
	require.NoError(t, tr.RecordRequestStarted("e1", "req-1"))

	summary := tr.Epochs(QueryFilter{EpochIndex: 15})
	require.Len(t, summary, 1)
	require.Equal(t, uint64(2), summary[0].InFlightRequests)

	require.NoError(t, tr.RecordRequestFinished("e1", "req-1"))
	require.Equal(t, uint64(1), tr.Epochs(QueryFilter{EpochIndex: 15})[0].InFlightRequests)
}

func TestSlotsCountOnlyTheirOwnRequests(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 15, "m")
	require.NoError(t, tr.RecordRequestStarted("e1", "req-1"))
	require.NoError(t, tr.RecordRequestStarted("e1", "req-2"))
	recordAttempt(t, tr, "e1", 1, "req-1")
	recordAttempt(t, tr, "e1", 2, "req-2")

	records := tr.Query(QueryFilter{EpochIndex: 15})
	require.Equal(t, uint64(1), onlyRecord(t, records, "p1").InFlightRequests, "slot 1 holds req-1 only")
	require.Equal(t, uint64(1), onlyRecord(t, records, "p0").InFlightRequests, "slot 0 holds req-2 only")
	for _, slot := range onlyRecord(t, records, "p1").Slots {
		require.Equal(t, uint64(1), slot.InFlightRequests, "slot %d reports its own open request", slot.SlotID)
	}
}

func TestACommittedButUndispatchedNonceHoldsNoRequest(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 15, "m")
	require.NoError(t, tr.RecordRequestStarted("e1", "req-1"))
	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordRequestID("e1", 1, "req-1"))

	serving := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 15}), "p1")
	require.Zero(t, serving.InFlight)
	require.Zero(t, serving.InFlightRequests, "a nonce the gateway never dispatched puts no work on the host")
}

func TestAnEmptyRequestIDNeverOpensAnything(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 15, "m")
	require.NoError(t, tr.RecordRequestStarted("e1", ""))

	require.Zero(t, tr.Epochs(QueryFilter{EpochIndex: 15})[0].InFlightRequests)
}

func warmupProbe(t *testing.T, tr *Tracker, escrowID string, nonce uint64, settle bool) {
	t.Helper()
	require.NoError(t, tr.RecordDiff(escrowID, nonce, true))
	require.NoError(t, tr.RecordRealSend(escrowID, nonce, accountingTestNow, PhaseNormal, QuarantineNone))
	require.NoError(t, tr.RecordProtocol(escrowID, nonce, uint32(nonce%2), ProtocolFinishApplied, types.HostStats{}))
	if settle {
		require.NoError(t, tr.RecordUsage(escrowID, nonce, UsageLoser, ""))
	}
}

func TestAFullWarmupLeavesNothingUnclassified(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 15, "m")
	warmupProbe(t, tr, "e1", 1, true)
	warmupProbe(t, tr, "e1", 2, true)

	var assigned, unclassified, pending, unused uint64
	for _, record := range tr.Query(QueryFilter{EpochIndex: 15}) {
		assigned += record.AssignedNonces
		unclassified += record.Unclassified
		pending += record.PendingClassification
		unused += record.Dispositions[DispositionFinishedUnused]
	}
	require.Equal(t, uint64(2), assigned, "both warmup nonces count as assigned work")
	require.Equal(t, uint64(2), unused, "and both settle as work nobody used")
	require.Zero(t, unclassified)
	require.Zero(t, pending)
}

func TestAnUnsettledWarmupProbeIsWhatLeaksIntoPending(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 15, "m")
	warmupProbe(t, tr, "e1", 1, false)

	record := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 15}), "p1")
	require.Equal(t, uint64(1), record.AssignedNonces)
	require.Equal(t, uint64(1), record.PendingClassification,
		"a finished nonce with no usage matches no disposition and never leaves the live set")
	require.Zero(t, record.Dispositions[DispositionFinishedUnused])
}

func TestARefusedWarmupProbeSettlesAtItsDeadline(t *testing.T) {
	tr := newTestTracker(t)
	now := accountingTestNow
	tr.now = func() time.Time { return now }
	registerEscrow(t, tr, "e1", 15, "m")
	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordRealSend("e1", 1, now, PhaseNormal, QuarantineNone))
	require.NoError(t, tr.RecordProtocol("e1", 1, 1, ProtocolTimeoutApplied, types.HostStats{Missed: 1}))

	waiting := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 15}), "p1")
	require.Equal(t, uint64(1), waiting.InFlight, "before its deadline the nonce is honestly still open")
	require.Zero(t, waiting.Unclassified)
	require.Zero(t, waiting.PendingClassification)

	now = accountingTestNow.Add(5 * time.Minute)
	settled := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 15}), "p1")
	require.Equal(t, uint64(1), settled.Dispositions[DispositionUnfinishedRefused])
	require.Zero(t, settled.InFlight)
	require.Zero(t, settled.Unclassified)
	require.Zero(t, settled.PendingClassification)
}

func TestWarmupDoesNotRaiseUnusedAnswersOnAHost(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 15, "m")
	for nonce := uint64(1); nonce <= 60; nonce++ {
		require.NoError(t, tr.RecordDiff("e1", nonce, true))
		require.NoError(t, tr.RecordRealSend("e1", nonce, accountingTestNow, PhaseNormal, QuarantineNone))
		require.NoError(t, tr.RecordProtocol("e1", nonce, uint32(nonce%2), ProtocolFinishApplied, types.HostStats{}))
		require.NoError(t, tr.RecordUsage("e1", nonce, UsageLoser, DeliveryWarmupProbe))
	}

	record := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 15}), "p1")
	require.NotZero(t, record.Dispositions[DispositionFinishedUnused], "the probes are still counted as work")
	for _, finding := range record.Findings {
		require.NotEqual(t, FindingUnusedAnswers, finding.Code,
			"an escrow whose only traffic is warmup must not read as a host nobody uses")
	}
}

func TestRealUnusedAnswersStillRaiseTheFinding(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 15, "m")
	for nonce := uint64(1); nonce <= 60; nonce++ {
		require.NoError(t, tr.RecordDiff("e1", nonce, true))
		require.NoError(t, tr.RecordRealSend("e1", nonce, accountingTestNow, PhaseNormal, QuarantineNone))
		require.NoError(t, tr.RecordProtocol("e1", nonce, uint32(nonce%2), ProtocolFinishApplied, types.HostStats{}))
		require.NoError(t, tr.RecordUsage("e1", nonce, UsageLoser, ""))
	}

	record := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 15}), "p1")
	kinds := make([]string, 0, len(record.Findings))
	for _, finding := range record.Findings {
		kinds = append(kinds, finding.Code)
	}
	require.Contains(t, kinds, FindingUnusedAnswers)
}

func TestProbeServedMarksTheNonceAsTheGatewaysOwnWork(t *testing.T) {
	tr := newTestTracker(t)
	recorder := NewRecorder(tr, nil)
	registerEscrow(t, tr, "e1", 15, "m")
	for nonce := uint64(1); nonce <= 60; nonce++ {
		require.NoError(t, tr.RecordDiff("e1", nonce, true))
		require.NoError(t, tr.RecordRealSend("e1", nonce, accountingTestNow, PhaseNormal, QuarantineNone))
		require.NoError(t, tr.RecordProtocol("e1", nonce, uint32(nonce%2), ProtocolFinishApplied, types.HostStats{}))
		recorder.ProbeServed("e1", nonce, DeliveryWarmupProbe)
	}

	record := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 15}), "p1")
	require.NotZero(t, record.Dispositions[DispositionFinishedUnused])
	for _, finding := range record.Findings {
		require.NotEqual(t, FindingUnusedAnswers, finding.Code,
			"ProbeServed must stamp the reason findings filter on, not just settle the nonce")
	}
}

// A probe is work the host did that nobody asked for: counted as work, kept out of the user ratios.
func TestServedThrottleProbesDoNotRaiseUnusedAnswersOnAHost(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 15, "m")
	for nonce := uint64(1); nonce <= 60; nonce++ {
		require.NoError(t, tr.RecordDiff("e1", nonce, true))
		require.NoError(t, tr.RecordProbeSend("e1", nonce, accountingTestNow, PhaseNormal, QuarantineNone, DeliveryThrottleProbe))
		require.NoError(t, tr.RecordProtocol("e1", nonce, uint32(nonce%2), ProtocolFinishApplied, types.HostStats{}))
		require.NoError(t, tr.RecordUsage("e1", nonce, UsageLoser, DeliveryThrottleProbe))
	}

	record := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 15}), "p1")
	require.NotZero(t, record.Dispositions[DispositionFinishedUnused], "the probes are still counted as work")
	require.Zero(t, record.Unclassified, "a served probe that finishes must classify, not fall through")
	for _, finding := range record.Findings {
		require.NotEqual(t, FindingUnusedAnswers, finding.Code,
			"an escrow whose only traffic is probes must not read as a host nobody uses")
	}
}

// Probing a host must not raise its refusal rate by the act of measuring it.
func TestUnservedThrottleProbesDoNotRaiseRefusalsOnAHost(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 15, "m")
	for nonce := uint64(1); nonce <= 60; nonce++ {
		require.NoError(t, tr.RecordDiff("e1", nonce, true))
		require.NoError(t, tr.RecordProbeSend("e1", nonce, accountingTestNow.Add(-2*time.Minute), PhaseNormal, QuarantineNone, DeliveryThrottleProbe))
	}

	record := onlyRecord(t, tr.Query(QueryFilter{EpochIndex: 15}), "p1")
	require.NotZero(t, record.Dispositions[DispositionUnfinishedRefused],
		"the probes must be classified, or this asserts nothing")
	for _, finding := range record.Findings {
		require.NotEqual(t, FindingRefusals, finding.Code,
			"probing a host must not raise its refusal rate by the act of measuring it")
	}
}
