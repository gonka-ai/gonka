package accounting

import (
	"context"
	"testing"
	"time"

	"devshard/types"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

// Every case drives the real tracker and reads Query, because a finding derived from a hand-built
// record would still pass with nothing calling findingsFor.
func refuseNonces(t *testing.T, tr *Tracker, escrowID string, nonces []uint64, origin FailureOrigin) {
	t.Helper()
	for _, nonce := range nonces {
		require.NoError(t, tr.RecordDiff(escrowID, nonce, true))
		require.NoError(t, tr.RecordRealSend(escrowID, nonce, accountingTestNow.Add(-2*time.Minute), PhaseNormal, QuarantineNone))
		require.NoError(t, tr.RecordTimeout(TimeoutRecord{
			EscrowID: escrowID, Nonce: nonce, Kind: TimeoutRefused, Phase: PhaseNormal,
			Outcome: TimeoutApplied, FailureOrigin: origin,
		}))
	}
}

func findingByCode(record ParticipantRecord, code string) (Finding, bool) {
	for _, finding := range record.Findings {
		if finding.Code == code {
			return finding, true
		}
	}
	return Finding{}, false
}

func recordFor(t *testing.T, tr *Tracker, participant string) ParticipantRecord {
	t.Helper()
	records := tr.Query(QueryFilter{EpochIndex: 7, Participant: participant})
	require.Len(t, records, 1)
	return records[0]
}

// Slot 0 takes the even nonces, so 60 of them put one participant far past both the minimum volume
// and the critical refusal rate while the other participant stays empty.
func TestRefusingHostIsFlaggedCriticalThroughQuery(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	evenNonces := make([]uint64, 0, 30)
	for nonce := uint64(2); nonce <= 60; nonce += 2 {
		evenNonces = append(evenNonces, nonce)
	}
	refuseNonces(t, tr, "e1", evenNonces, FailureHostResponse)

	finding, found := findingByCode(recordFor(t, tr, "p0"), FindingRefusals)
	require.True(t, found, "a host that refused every nonce must be flagged")
	require.Equal(t, SeverityCritical, finding.Severity)
	require.Equal(t, uint64(30), finding.Part)
	require.Equal(t, uint64(30), finding.Whole)

	_, flaggedElsewhere := findingByCode(recordFor(t, tr, "p1"), FindingRefusals)
	require.False(t, flaggedElsewhere, "the other slot did nothing and must stay clean")
}

// The whole point of reading FailureOrigin: the same failures, attributed to this gateway's own
// policy, must not appear as the host's refusal rate.
func TestGatewayPolicyFailuresAreNotChargedToTheHost(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	evenNonces := make([]uint64, 0, 30)
	for nonce := uint64(2); nonce <= 60; nonce += 2 {
		evenNonces = append(evenNonces, nonce)
	}
	refuseNonces(t, tr, "e1", evenNonces, FailureGatewayPolicy)

	record := recordFor(t, tr, "p0")
	_, flagged := findingByCode(record, FindingRefusals)
	require.False(t, flagged, "a gateway-policy failure is not the host's refusal")
	require.Equal(t, uint64(30), record.Dispositions[DispositionUnfinishedRefused],
		"the disposition is still counted; only the finding excuses it")

	origins, found := findingByCode(record, FindingFailureOrigins)
	require.True(t, found, "the failures are still counted, they just are not charged to the host")
	require.Equal(t, uint64(30), origins.Part)
	require.Equal(t, uint64(30), origins.Whole)
	require.Equal(t, uint64(30), countersWhere(record, func(key CounterKey) bool {
		return key.FailureOrigin == FailureGatewayPolicy
	}), "the counters are where the breakdown lives now")
}

// The breakdown counts excused failures and the rates do not, so the two need different denominators.
// Sharing one produced "30 of 20", a numerator larger than its whole.
func TestFailureBreakdownNeverExceedsItsOwnDenominator(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	for nonce := uint64(2); nonce <= 60; nonce += 2 {
		origin := FailureHostResponse
		if nonce%6 == 0 {
			origin = FailureGatewayPolicy
		}
		refuseNonces(t, tr, "e1", []uint64{nonce}, origin)
	}

	record := recordFor(t, tr, "p0")
	origins, found := findingByCode(record, FindingFailureOrigins)
	require.True(t, found)
	require.Equal(t, uint64(30), origins.Part)
	require.Equal(t, uint64(30), origins.Whole, "the denominator counts excused failures too")
	require.Equal(t, uint64(20), countersWhere(record, func(key CounterKey) bool {
		return key.FailureOrigin == FailureHostResponse
	}))
	require.Equal(t, uint64(10), countersWhere(record, func(key CounterKey) bool {
		return key.FailureOrigin == FailureGatewayPolicy
	}))
}

func TestBelowMinimumVolumeNothingIsFlagged(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	refuseNonces(t, tr, "e1", []uint64{2, 4, 6, 8}, FailureHostResponse)

	record := recordFor(t, tr, "p0")
	require.Equal(t, uint64(4), record.Dispositions[DispositionUnfinishedRefused])
	require.Empty(t, record.Findings, "four nonces are too few to judge a host on")
}

// The chain's own verdict is a separate signal from this gateway's: a host can look clean here and
// still be recorded as missing on chain.
func TestChainRecordedMissesAreFlaggedFromHostStats(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	for nonce := uint64(2); nonce <= 60; nonce += 2 {
		require.NoError(t, tr.RecordDiff("e1", nonce, true))
	}
	require.NoError(t, tr.RecordProtocol("e1", 2, 0, ProtocolTimeoutApplied, types.HostStats{Missed: 9}))

	finding, found := findingByCode(recordFor(t, tr, "p0"), FindingProtocolMisses)
	require.True(t, found)
	require.Equal(t, SeverityCritical, finding.Severity)
	require.Equal(t, uint64(9), finding.Part)
	require.Equal(t, uint64(30), finding.Whole)
}

func TestFindingsReachPrometheus(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	evenNonces := make([]uint64, 0, 30)
	for nonce := uint64(2); nonce <= 60; nonce += 2 {
		evenNonces = append(evenNonces, nonce)
	}
	refuseNonces(t, tr, "e1", evenNonces, FailureHostResponse)

	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(NewCollector(tr, func(context.Context) (uint64, error) { return 7, nil })))
	families, err := registry.Gather()
	require.NoError(t, err)

	var labelled []string
	for _, family := range families {
		if family.GetName() != "devshard_accounting_finding" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labelled = append(labelled, labelValue(metric, "code")+"/"+labelValue(metric, "severity"))
			require.Equal(t, float64(1), metric.GetGauge().GetValue())
		}
	}
	require.Contains(t, labelled, FindingRefusals+"/"+string(SeverityCritical))
}

func labelValue(metric *dto.Metric, name string) string {
	for _, pair := range metric.GetLabel() {
		if pair.GetName() == name {
			return pair.GetValue()
		}
	}
	return ""
}

// deliverNonces drives a nonce all the way to a finished, used answer, so the denominator the decoded
// -logprobs rate divides by is the delivered count and not something a hand-built record invented.
func deliverNonces(t *testing.T, tr *Tracker, escrowID string, nonces []uint64, decodedLogprobs bool) {
	t.Helper()
	for _, nonce := range nonces {
		require.NoError(t, tr.RecordDiff(escrowID, nonce, true))
		require.NoError(t, tr.RecordRealSend(escrowID, nonce, accountingTestNow.Add(-2*time.Minute), PhaseNormal, QuarantineNone))
		if decodedLogprobs {
			require.NoError(t, tr.RecordLogprobsDecoded(escrowID, nonce))
		}
		require.NoError(t, tr.RecordUsage(escrowID, nonce, UsageWinner, ""))
		require.NoError(t, tr.RecordProtocol(escrowID, nonce, 0, ProtocolFinishApplied, types.HostStats{}))
	}
}

// A validator replays an answer from the token ids in its logprobs, so text there costs the host the
// reward on every inference it is sampled on. One in a hundred is already worth naming.
func TestAnswersReportingDecodedLogprobsAreFlagged(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	decoded := make([]uint64, 0, 30)
	for nonce := uint64(2); nonce <= 60; nonce += 2 {
		decoded = append(decoded, nonce)
	}
	deliverNonces(t, tr, "e1", decoded, true)

	finding, found := findingByCode(recordFor(t, tr, "p0"), FindingDecodedLogprobs)
	require.True(t, found, "a host reporting logprob text instead of ids must be flagged")
	require.Equal(t, SeverityCritical, finding.Severity)
	require.Equal(t, uint64(30), finding.Part)
	require.Equal(t, uint64(30), finding.Whole)
}

// The finding must name the defect, not the traffic: a host answering correctly stays clean however
// many answers it delivers.
func TestAnswersReportingTokenIDsAreNotFlagged(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	healthy := make([]uint64, 0, 30)
	for nonce := uint64(2); nonce <= 60; nonce += 2 {
		healthy = append(healthy, nonce)
	}
	deliverNonces(t, tr, "e1", healthy, false)

	record := recordFor(t, tr, "p0")
	_, flagged := findingByCode(record, FindingDecodedLogprobs)
	require.False(t, flagged, "a host reporting token ids has nothing to answer for")
	require.Equal(t, uint64(30), record.Dispositions[DispositionFinishedUsed])
}

// The ledger owns what counts as a fault, so the boundary is asserted here rather than at each caller.
func TestAttemptTimingClassifiesAgainstTheLedgersOwnThresholds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		timing           AttemptTiming
		wantSlowReceipt  bool
		wantSlowChunk    bool
		wantClockDrifted bool
	}{
		{name: "nothing measured"},
		{
			name:            "acknowledgement exactly at the threshold is not yet slow",
			timing:          AttemptTiming{Acknowledgement: SlowReceiptAfter},
			wantSlowReceipt: false,
		},
		{
			name:            "acknowledgement past the threshold",
			timing:          AttemptTiming{Acknowledgement: SlowReceiptAfter + time.Millisecond},
			wantSlowReceipt: true,
		},
		{
			name:          "a gap past the threshold",
			timing:        AttemptTiming{MaxChunkGap: SlowChunkGapAfter + time.Millisecond},
			wantSlowChunk: true,
		},
		{
			name:   "an offset nobody measured is not drift",
			timing: AttemptTiming{ClockOffset: time.Hour},
		},
		{
			name:             "a host running ahead",
			timing:           AttemptTiming{ClockOffset: ClockDriftBeyond + time.Second, ClockMeasured: true},
			wantClockDrifted: true,
		},
		{
			name:             "a host running behind fails the same way",
			timing:           AttemptTiming{ClockOffset: -ClockDriftBeyond - time.Second, ClockMeasured: true},
			wantClockDrifted: true,
		},
		{
			name:   "an offset inside the tolerance",
			timing: AttemptTiming{ClockOffset: ClockDriftBeyond - time.Second, ClockMeasured: true},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := testCase.timing.receiptWasSlow(); got != testCase.wantSlowReceipt {
				t.Errorf("receiptWasSlow() = %v, want %v", got, testCase.wantSlowReceipt)
			}
			if got := testCase.timing.chunkWasSlow(); got != testCase.wantSlowChunk {
				t.Errorf("chunkWasSlow() = %v, want %v", got, testCase.wantSlowChunk)
			}
			if got := testCase.timing.clockHasDrifted(); got != testCase.wantClockDrifted {
				t.Errorf("clockHasDrifted() = %v, want %v", got, testCase.wantClockDrifted)
			}
		})
	}
}

// Driven through the real tracker, because a finding derived from a hand-built record would pass with
// nothing carrying the fact from the attempt to the counter key.
func TestTimingFaultsReachTheFindingsThroughQuery(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	nonces := make([]uint64, 0, 30)
	for nonce := uint64(2); nonce <= 60; nonce += 2 {
		nonces = append(nonces, nonce)
	}
	for _, nonce := range nonces {
		require.NoError(t, tr.RecordDiff("e1", nonce, true))
		require.NoError(t, tr.RecordRealSend("e1", nonce, accountingTestNow.Add(-2*time.Minute), PhaseNormal, QuarantineNone))
		require.NoError(t, tr.RecordAttemptTiming("e1", nonce, AttemptTiming{
			Acknowledgement: SlowReceiptAfter + time.Second,
			MaxChunkGap:     SlowChunkGapAfter + time.Second,
			ClockOffset:     ClockDriftBeyond + time.Second,
			ClockMeasured:   true,
		}))
		require.NoError(t, tr.RecordUsage("e1", nonce, UsageWinner, ""))
		require.NoError(t, tr.RecordProtocol("e1", nonce, 0, ProtocolFinishApplied, types.HostStats{}))
	}

	record := recordFor(t, tr, "p0")
	for _, code := range []string{FindingSlowReceipts, FindingSlowChunks, FindingClockDrift} {
		finding, found := findingByCode(record, code)
		require.Truef(t, found, "%s must be flagged when every attempt showed it", code)
		require.Equalf(t, uint64(30), finding.Part, "%s must count every attempt", code)
		require.Equalf(t, uint64(30), finding.Whole, "%s must measure against every attempt", code)
	}
}

// A healthy host must stay clean: the findings name the fault, not the traffic.
func TestTimingWithinToleranceIsNotFlagged(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	for nonce := uint64(2); nonce <= 60; nonce += 2 {
		require.NoError(t, tr.RecordDiff("e1", nonce, true))
		require.NoError(t, tr.RecordRealSend("e1", nonce, accountingTestNow.Add(-2*time.Minute), PhaseNormal, QuarantineNone))
		require.NoError(t, tr.RecordAttemptTiming("e1", nonce, AttemptTiming{
			Acknowledgement: time.Second, MaxChunkGap: time.Millisecond, ClockMeasured: true,
		}))
		require.NoError(t, tr.RecordUsage("e1", nonce, UsageWinner, ""))
		require.NoError(t, tr.RecordProtocol("e1", nonce, 0, ProtocolFinishApplied, types.HostStats{}))
	}

	record := recordFor(t, tr, "p0")
	for _, code := range []string{FindingSlowReceipts, FindingSlowChunks, FindingClockDrift} {
		_, flagged := findingByCode(record, code)
		require.Falsef(t, flagged, "%s must not fire on a host inside every tolerance", code)
	}
}

// A settlement submits six numbers per slot. Five are already read from the same host statistics; the
// validation pair completes the set, so the service can show what settling would record without settling.
func TestValidationCountsReachTheRecordFromHostStats(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	for nonce := uint64(2); nonce <= 60; nonce += 2 {
		require.NoError(t, tr.RecordDiff("e1", nonce, true))
	}
	require.NoError(t, tr.RecordHostStats("e1", 0, types.HostStats{
		Missed: 3, Invalid: 2, RequiredValidations: 12, CompletedValidations: 7,
	}))

	record := recordFor(t, tr, "p0")

	require.Equal(t, uint64(12), record.RequiredValidations)
	require.Equal(t, uint64(7), record.CompletedValidations)
	require.Equal(t, uint64(3), record.ProtocolMisses, "the pair travels with the counters beside it")
	require.Equal(t, uint64(2), record.ProtocolInvalid)
}
