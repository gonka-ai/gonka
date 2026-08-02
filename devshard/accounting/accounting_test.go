package accounting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"devshard/types"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestTrackerCountsAndCrossChecks(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordGhost("e1", 1, PhasePoC, QuarantineProbe, NoSendParticipantThrottled, ""))
	require.NoError(t, tr.RecordDiff("e1", 2, true))
	require.NoError(t, tr.RecordRealSend("e1", 2, PhaseNormal, QuarantineShadow))
	require.NoError(t, tr.RecordUsage("e1", 2, UsageWinner))
	require.NoError(t, tr.RecordProtocol("e1", 2, 0, ProtocolFinishApplied, types.HostStats{}))
	require.NoError(t, tr.RecordDiff("e1", 3, false))
	require.NoError(t, tr.RecordHostStats("e1", 1, types.HostStats{Missed: 1}))
	require.NoError(t, tr.ReconcileAppliedMisses("e1"))

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
	registerEscrow(t, tr, "e1", 8, "m")
	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordRealSend("e1", 1, PhaseNormal, QuarantineNone))
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
	handler := NewHandler(tr, func(context.Context) (uint64, error) { return 9, nil })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/epochs/current/participants?model=m1&escrow_id=e1,e2", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Participants []ParticipantRecord `json:"participants"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
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
}

func TestNonExecutionCreditIdentity(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 10, "m")
	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordGhost("e1", 1, PhaseNormal, QuarantineNone, NoSendPoCUnavailable, ""))
	require.NoError(t, tr.RecordDiff("e1", 2, false))
	require.NoError(t, tr.RecordDiff("e1", 3, true))
	require.NoError(t, tr.RecordRealSend("e1", 3, PhaseNormal, QuarantineNone))
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

func newTestTracker(t *testing.T) *Tracker {
	t.Helper()
	tr, err := OpenTracker(filepath.Join(t.TempDir(), "accounting.db"), 0, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tr.Close()) })
	return tr
}

func registerEscrow(t *testing.T, tr *Tracker, id string, epoch uint64, model string) {
	t.Helper()
	require.NoError(t, tr.RegisterEscrow(EscrowMetadata{
		EscrowID:      id,
		CreationEpoch: epoch,
		Model:         model,
		Phase:         EscrowActive,
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
