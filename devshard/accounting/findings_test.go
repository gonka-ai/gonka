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
	require.Equal(t, "30 of 30 nonces were never acknowledged (100.0%)", finding.Observed)
	require.NotEmpty(t, finding.Detail)

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
	require.True(t, found, "the breakdown still names them, it just does not charge them to the host")
	require.Equal(t, "30 of 30 nonces reached this participant and produced no usable answer: gateway_policy (30)", origins.Observed)
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

	origins, found := findingByCode(recordFor(t, tr, "p0"), FindingFailureOrigins)
	require.True(t, found)
	require.Equal(t, "30 of 30 nonces reached this participant and produced no usable answer: host_response (20), gateway_policy (10)", origins.Observed)
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
	require.Contains(t, finding.Observed, "9 of 30 assigned nonces were recorded as missed on chain")
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
