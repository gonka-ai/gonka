package accounting

import (
	"sync"
	"testing"

	"devshard/types"

	"github.com/stretchr/testify/require"
)

func TestBookTransitionsAndIdempotency(t *testing.T) {
	book := NewBook()
	registerTestEscrow(t, book, "escrow-1", 7, "model-a", EscrowActive)

	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 1, Inference: true}))
	ghost := Ghost{
		EscrowID:      "escrow-1",
		Nonce:         1,
		DispatchPhase: PhasePoC,
		Quarantine:    QuarantineProbe,
		Reason:        NoSendParticipantThrottled,
		DetailReason:  "probe_quarantine",
	}
	require.NoError(t, book.Apply(ghost))
	require.NoError(t, book.Apply(ghost))

	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 2}))

	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 3, Inference: true}))
	require.NoError(t, book.Apply(RealSend{
		EscrowID:      "escrow-1",
		Nonce:         3,
		DispatchPhase: PhaseNormal,
		Quarantine:    QuarantineShadow,
	}))
	require.NoError(t, book.Apply(ProtocolTransition{
		EscrowID: "escrow-1",
		Nonce:    3,
		Kind:     ProtocolReceiptApplied,
	}))
	require.NoError(t, book.Apply(TimeoutRequired{
		EscrowID:        "escrow-1",
		Nonce:           3,
		Kind:            TimeoutExecution,
		EvaluationPhase: PhaseNormal,
		FailureOrigin:   FailureHostResponse,
		DetailReason:    "stream_stalled",
	}))
	require.NoError(t, book.Apply(TimeoutOutcomeRecorded{
		EscrowID: "escrow-1",
		Nonce:    3,
		Outcome:  TimeoutVoteCollectionFailed,
		Reason:   TimeoutContextCanceled,
	}))

	records := book.Query(QueryFilter{EpochIndex: 7})
	require.Len(t, records, 2)
	hostOne := participantRecord(t, records, "participant-1")
	require.Equal(t, uint64(1), hostOne.Dispositions[DispositionGhost])
	require.Equal(t, uint64(1), hostOne.Dispositions[DispositionUnfinishedExecution])
	require.Equal(t, uint64(1), hostOne.TimeoutOutcomes[TimeoutVoteCollectionFailed])

	applied := TimeoutOutcomeRecorded{
		EscrowID: "escrow-1",
		Nonce:    3,
		Outcome:  TimeoutApplied,
	}
	require.NoError(t, book.Apply(applied))
	require.NoError(t, book.Apply(applied))
	require.NoError(t, book.Apply(HostStatsObserved{
		EscrowID: "escrow-1",
		SlotID:   1,
		Stats:    types.HostStats{Missed: 1},
	}))

	records = book.Query(QueryFilter{EpochIndex: 7})
	hostOne = participantRecord(t, records, "participant-1")
	require.Zero(t, hostOne.TimeoutOutcomes[TimeoutVoteCollectionFailed])
	require.Equal(t, uint64(1), hostOne.TimeoutOutcomes[TimeoutApplied])
	require.Zero(t, hostOne.CrossChecks.ErrorCount)
	require.Zero(t, book.LiveNonceCount())
}

func TestTimeoutEligibilityMovesFromRefusalToExecution(t *testing.T) {
	book := NewBook()
	registerTestEscrow(t, book, "escrow-1", 7, "model-a", EscrowActive)
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 1, Inference: true}))
	require.NoError(t, book.Apply(RealSend{EscrowID: "escrow-1", Nonce: 1}))
	require.NoError(t, book.Apply(TimeoutRequired{
		EscrowID: "escrow-1", Nonce: 1, Kind: TimeoutRefused,
		EvaluationPhase: PhaseNormal,
	}))
	require.NoError(t, book.Apply(TimeoutRequired{
		EscrowID: "escrow-1", Nonce: 1, Kind: TimeoutExecution,
		EvaluationPhase: PhasePoC,
	}))

	record := participantRecord(t, book.Query(QueryFilter{EpochIndex: 7}), "participant-1")
	require.Len(t, record.Counters, 1)
	require.Equal(t, TimeoutExecution, record.Counters[0].TimeoutKind)
	require.Equal(t, PhasePoC, record.Counters[0].TimeoutEvaluationPhase)
}

func TestBookCountsRejectedEvents(t *testing.T) {
	book := NewBook()
	registerTestEscrow(t, book, "escrow-1", 7, "model-a", EscrowActive)
	require.Error(t, book.Apply(RealSend{EscrowID: "escrow-1", Nonce: 1}))

	record := participantRecord(t, book.Query(QueryFilter{EpochIndex: 7}), "participant-1")
	require.Equal(t, uint64(1), record.RecordingErrors)
}

func TestBookRecoveryDerivesUnclassifiedFromLatestNonce(t *testing.T) {
	book := NewBook()
	registerTestEscrow(t, book, "escrow-1", 9, "model-a", EscrowActive)
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 1, Inference: true}))
	require.NoError(t, book.Apply(RealSend{
		EscrowID:      "escrow-1",
		Nonce:         1,
		DispatchPhase: PhaseNormal,
	}))

	before := participantRecord(t, book.Query(QueryFilter{EpochIndex: 9}), "participant-1")
	require.Equal(t, uint64(1), before.InFlight)
	require.Zero(t, before.Unclassified)

	recovered, err := NewBookFromSnapshot(book.Snapshot())
	require.NoError(t, err)
	after := participantRecord(t, recovered.Query(QueryFilter{EpochIndex: 9}), "participant-1")
	require.Zero(t, after.InFlight)
	require.Equal(t, uint64(1), after.Unclassified)
}

func TestBookRecoveryKeepsRetryableTimeout(t *testing.T) {
	book := NewBook()
	registerTestEscrow(t, book, "escrow-1", 9, "model-a", EscrowActive)
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 1, Inference: true}))
	require.NoError(t, book.Apply(RealSend{EscrowID: "escrow-1", Nonce: 1, DispatchPhase: PhaseNormal}))
	require.NoError(t, book.Apply(TimeoutRequired{
		EscrowID:        "escrow-1",
		Nonce:           1,
		Kind:            TimeoutRefused,
		EvaluationPhase: PhaseNormal,
		FailureOrigin:   FailureHostResponse,
		DetailReason:    "no_receipt",
	}))
	require.NoError(t, book.Apply(TimeoutOutcomeRecorded{
		EscrowID: "escrow-1",
		Nonce:    1,
		Outcome:  TimeoutInsufficientVotes,
		Reason:   TimeoutReasonUnknown,
	}))

	recovered, err := NewBookFromSnapshot(book.Snapshot())
	require.NoError(t, err)
	require.Equal(t, 1, recovered.LiveNonceCount())
	require.NoError(t, recovered.Apply(TimeoutOutcomeRecorded{
		EscrowID: "escrow-1",
		Nonce:    1,
		Outcome:  TimeoutApplied,
	}))
	require.NoError(t, recovered.Apply(HostStatsObserved{
		EscrowID: "escrow-1",
		SlotID:   1,
		Stats:    types.HostStats{Missed: 1},
	}))
	record := participantRecord(t, recovered.Query(QueryFilter{EpochIndex: 9}), "participant-1")
	require.Equal(t, uint64(1), record.TimeoutOutcomes[TimeoutApplied])
	require.Zero(t, record.CrossChecks.ErrorCount)
	require.Zero(t, recovered.LiveNonceCount())
}

func TestBookRestoreRejectsPendingTimeoutWithoutCounter(t *testing.T) {
	book := NewBook()
	registerTestEscrow(t, book, "escrow-1", 9, "model-a", EscrowActive)
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 1, Inference: true}))
	require.NoError(t, book.Apply(RealSend{EscrowID: "escrow-1", Nonce: 1}))
	require.NoError(t, book.Apply(TimeoutRequired{
		EscrowID: "escrow-1", Nonce: 1, Kind: TimeoutRefused,
		FailureOrigin: FailureHostResponse, DetailReason: "no_receipt",
	}))

	snapshot := book.Snapshot()
	require.Len(t, snapshot.Escrows[0].Pending, 1)
	delete(snapshot.Escrows[0].Counters, snapshot.Escrows[0].Pending[0].Counted)
	_, err := NewBookFromSnapshot(snapshot)
	require.ErrorContains(t, err, "references a missing counter")
}

func TestLateFinishClearsNonAppliedTimeout(t *testing.T) {
	book := NewBook()
	registerTestEscrow(t, book, "escrow-1", 9, "model-a", EscrowActive)
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 1, Inference: true}))
	require.NoError(t, book.Apply(RealSend{EscrowID: "escrow-1", Nonce: 1}))
	require.NoError(t, book.Apply(TimeoutRequired{
		EscrowID: "escrow-1", Nonce: 1, Kind: TimeoutRefused,
		FailureOrigin: FailureHostResponse, DetailReason: "no_receipt",
	}))
	require.NoError(t, book.Apply(TimeoutOutcomeRecorded{
		EscrowID: "escrow-1", Nonce: 1, Outcome: TimeoutInsufficientVotes,
	}))
	require.NoError(t, book.Apply(Winner{EscrowID: "escrow-1", Nonce: 1}))
	require.NoError(t, book.Apply(ProtocolTransition{
		EscrowID: "escrow-1", Nonce: 1, Kind: ProtocolFinishApplied,
	}))

	record := participantRecord(t, book.Query(QueryFilter{EpochIndex: 9}), "participant-1")
	require.Equal(t, uint64(1), record.Dispositions[DispositionFinishedUsed])
	require.Empty(t, record.TimeoutOutcomes)
}

func TestBookFinishedUsageAndInvalidTransitions(t *testing.T) {
	book := NewBook()
	registerTestEscrow(t, book, "escrow-1", 10, "model-a", EscrowActive)
	usageEvents := []Event{
		Winner{EscrowID: "escrow-1", Nonce: 1},
		Loser{EscrowID: "escrow-1", Nonce: 2},
		UsageUnknown{EscrowID: "escrow-1", Nonce: 3},
	}
	for index, usage := range usageEvents {
		nonce := uint64(index + 1)
		require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: nonce, Inference: true}))
		require.NoError(t, book.Apply(&RealSend{
			EscrowID:      "escrow-1",
			Nonce:         nonce,
			DispatchPhase: PhaseNormal,
			Quarantine:    QuarantineProbation,
		}))
		require.NoError(t, book.Apply(usage))
		require.NoError(t, book.Apply(ProtocolTransition{
			EscrowID: "escrow-1",
			Nonce:    nonce,
			Kind:     ProtocolFinishApplied,
		}))
	}
	require.NoError(t, book.Apply(ProtocolTransition{
		EscrowID: "escrow-1",
		Nonce:    1,
		Kind:     ProtocolChallenged,
	}))
	require.NoError(t, book.Apply(ProtocolTransition{
		EscrowID: "escrow-1",
		Nonce:    1,
		Kind:     ProtocolInvalidated,
	}))
	require.NoError(t, book.Apply(HostStatsObserved{
		EscrowID: "escrow-1",
		SlotID:   1,
		Stats:    types.HostStats{Invalid: 1},
	}))

	records := book.Query(QueryFilter{EpochIndex: 10})
	var dispositions = make(map[Disposition]uint64)
	var recordedInvalid, unresolved, errors uint64
	for _, record := range records {
		for disposition, count := range record.Dispositions {
			dispositions[disposition] += count
		}
		recordedInvalid += record.CrossChecks.RecordedInvalid
		unresolved += record.UnresolvedChallenges
		errors += record.CrossChecks.ErrorCount
	}
	require.Equal(t, uint64(1), dispositions[DispositionFinishedUsed])
	require.Equal(t, uint64(1), dispositions[DispositionFinishedUnused])
	require.Equal(t, uint64(1), dispositions[DispositionFinishedUsageUnknown])
	require.Equal(t, uint64(1), recordedInvalid)
	require.Zero(t, unresolved)
	require.Zero(t, errors)
	require.Zero(t, book.LiveNonceCount())
}

func TestAssignedNoncesForSlotMatchesChainFormula(t *testing.T) {
	tests := []struct {
		latest uint64
		slot   uint32
		want   uint64
	}{
		{latest: 0, slot: 0, want: 0},
		{latest: 1, slot: 0, want: 0},
		{latest: 1, slot: 1, want: 1},
		{latest: 4, slot: 0, want: 1},
		{latest: 9, slot: 1, want: 3},
		{latest: 9, slot: 3, want: 2},
	}
	for _, test := range tests {
		got, err := AssignedNoncesForSlot(test.latest, 4, test.slot)
		require.NoError(t, err)
		require.Equal(t, test.want, got)
	}
	require.Equal(t, uint32(0), AssignedNonceSlot(4, 4))
	require.Equal(t, uint32(3), AssignedNonceSlot(7, 4))
	_, err := AssignedNoncesForSlot(1, 0, 0)
	require.Error(t, err)
	_, err = AssignedNoncesForSlot(1, 1, 1)
	require.Error(t, err)
}

func TestQueryReportsOverclassification(t *testing.T) {
	book := NewBook()
	registerTestEscrow(t, book, "escrow-1", 7, "model-a", EscrowActive)
	book.mu.Lock()
	escrow := book.escrows["escrow-1"]
	escrow.latest = 1
	escrow.counters[CounterKey{
		SlotID:      1,
		Disposition: DispositionProtocolOnly,
	}] = 2
	book.mu.Unlock()

	record := participantRecord(t, book.Query(QueryFilter{EpochIndex: 7}), "participant-1")
	require.Equal(t, uint64(1), record.Overclassified)
	require.Equal(t, uint64(1), record.CrossChecks.ErrorCount)
}

func TestBookConcurrentApplyAndQuery(t *testing.T) {
	book := NewBook()
	registerTestEscrow(t, book, "escrow-1", 11, "model-a", EscrowActive)

	var wait sync.WaitGroup
	for nonce := uint64(1); nonce <= 100; nonce++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := book.Apply(DiffApplied{
				EscrowID: "escrow-1",
				Nonce:    nonce,
			}); err != nil {
				t.Errorf("apply nonce %d: %v", nonce, err)
			}
			_ = book.Query(QueryFilter{EpochIndex: 11})
		}()
	}
	wait.Wait()

	var protocolOnly uint64
	for _, record := range book.Query(QueryFilter{EpochIndex: 11}) {
		protocolOnly += record.Dispositions[DispositionProtocolOnly]
	}
	require.Equal(t, uint64(100), protocolOnly)
}

func TestDiffAndInvalidationReplayRemainIdempotentPast4096Events(t *testing.T) {
	book := NewBook()
	registerTestEscrow(t, book, "escrow-1", 12, "model-a", EscrowActive)
	for nonce := uint64(1); nonce <= 5000; nonce++ {
		require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: nonce}))
	}
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 1}))

	record := participantRecord(t, book.Query(QueryFilter{EpochIndex: 12}), "participant-1")
	require.Equal(t, uint64(2500), record.Dispositions[DispositionProtocolOnly])

	require.NoError(t, book.Apply(ProtocolTransition{
		EscrowID: "escrow-1", Nonce: 1, Kind: ProtocolChallenged,
	}))
	require.NoError(t, book.Apply(ProtocolTransition{
		EscrowID: "escrow-1", Nonce: 1, Kind: ProtocolInvalidated,
	}))
	for range 5000 {
		require.NoError(t, book.Apply(ProtocolTransition{
			EscrowID: "escrow-1", Nonce: 1, Kind: ProtocolInvalidated,
		}))
	}
	record = participantRecord(t, book.Query(QueryFilter{EpochIndex: 12}), "participant-1")
	require.Equal(t, uint64(1), record.CrossChecks.RecordedInvalid)
}

func registerTestEscrow(t *testing.T, book *Book, id string, epoch uint64, model string, phase EscrowPhase) {
	t.Helper()
	require.NoError(t, book.Apply(EscrowRegistered{Metadata: EscrowMetadata{
		EscrowID:      id,
		CreationEpoch: epoch,
		Model:         model,
		Phase:         phase,
		Slots: []types.SlotAssignment{
			{SlotID: 0, ValidatorAddress: "participant-0"},
			{SlotID: 1, ValidatorAddress: "participant-1"},
		},
	}}))
}

func participantRecord(t *testing.T, records []ParticipantRecord, participant string) ParticipantRecord {
	t.Helper()
	for _, record := range records {
		if record.Participant == participant {
			return record
		}
	}
	t.Fatalf("participant %q not found", participant)
	return ParticipantRecord{}
}
