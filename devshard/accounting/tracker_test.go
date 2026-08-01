package accounting

import (
	"container/heap"
	"testing"
	"time"

	"devshard/types"

	"github.com/stretchr/testify/require"
)

type testProtocolView struct {
	inferences map[uint64]types.InferenceRecord
	state      types.EscrowState
}

func (v testProtocolView) GetCommittedRecord(nonce uint64) (types.InferenceRecord, bool) {
	record, ok := v.inferences[nonce]
	return record, ok
}

func (v testProtocolView) SnapshotStateNoInferences() types.EscrowState {
	return v.state
}

func newTestTracker(now time.Time) *Tracker {
	tracker := &Service{
		Book:     NewBook(),
		timeouts: make(map[string]timeoutConfig),
	}
	tracker.deadlines = newDeadlineScheduler(tracker.onDeadline)
	tracker.deadlines.now = func() time.Time { return now }
	return tracker
}

func TestRecordCommittedDiffParsesProtocolTransitionsAndHostStats(t *testing.T) {
	now := time.Unix(1_000, 0)
	tracker := newTestTracker(now)
	metadata := EscrowMetadata{
		EscrowID:      "escrow-1",
		CreationEpoch: 14,
		Model:         "model-a",
		Slots: []types.SlotAssignment{
			{SlotID: 0, ValidatorAddress: "participant-0"},
			{SlotID: 1, ValidatorAddress: "participant-1"},
		},
	}
	require.NoError(t, tracker.RegisterEscrow(metadata, types.SessionConfig{
		RefusalTimeout: 10, ExecutionTimeout: 20,
	}))

	start := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{
		StartInference: &types.MsgStartInference{InferenceId: 1, StartedAt: now.Unix()},
	}}
	require.NoError(t, tracker.RecordCommittedDiff(
		"escrow-1",
		types.Diff{Nonce: 1, Txs: []*types.DevshardTx{start}},
		testProtocolView{},
	))
	require.NoError(t, tracker.RecordRealSend("escrow-1", 1, PhasePoC, QuarantineProbe))

	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{
		ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ConfirmedAt: now.Unix()},
	}}
	require.NoError(t, tracker.RecordCommittedDiff(
		"escrow-1",
		types.Diff{Nonce: 2, Txs: []*types.DevshardTx{confirm}},
		testProtocolView{},
	))
	entry := tracker.deadlines.entries[deadlineKey{escrowID: "escrow-1", nonce: 1}]
	require.NotNil(t, entry)
	require.Equal(t, TimeoutExecution, entry.kind)
	require.Equal(t, now.Add(20*time.Second), entry.at)

	require.NoError(t, tracker.RecordUsage("escrow-1", 1, UsageWinner))
	finish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{
		FinishInference: &types.MsgFinishInference{InferenceId: 1, ExecutorSlot: 1},
	}}
	require.NoError(t, tracker.RecordCommittedDiff(
		"escrow-1",
		types.Diff{Nonce: 3, Txs: []*types.DevshardTx{finish}},
		testProtocolView{},
	))
	require.NotContains(t, tracker.deadlines.entries, deadlineKey{escrowID: "escrow-1", nonce: 1})

	validation := &types.DevshardTx{Tx: &types.DevshardTx_Validation{
		Validation: &types.MsgValidation{InferenceId: 1, ValidatorSlot: 0},
	}}
	view := testProtocolView{
		inferences: map[uint64]types.InferenceRecord{
			1: {Status: types.StatusInvalidated},
		},
		state: types.EscrowState{HostStats: map[uint32]*types.HostStats{
			0: {RequiredValidations: 1, CompletedValidations: 1},
			1: {Invalid: 1, Cost: 7},
		}},
	}
	diff := types.Diff{Nonce: 4, Txs: []*types.DevshardTx{validation}}
	require.NoError(t, tracker.RecordCommittedDiff("escrow-1", diff, view))
	require.NoError(t, tracker.RecordCommittedDiff("escrow-1", diff, view))

	record := participantRecord(t, tracker.Book.Query(QueryFilter{EpochIndex: 14}), "participant-1")
	require.Equal(t, uint64(1), record.Dispositions[DispositionFinishedUsed])
	require.Equal(t, uint64(1), record.ProtocolInvalid)
	require.Equal(t, uint64(1), record.CrossChecks.RecordedInvalid)
	require.Zero(t, record.CrossChecks.ErrorCount)
}

func TestRecordCommittedDiffRejectsGapsWithoutAdvancingWatermark(t *testing.T) {
	tracker := newTestTracker(time.Unix(1_500, 0))
	require.NoError(t, tracker.RegisterEscrow(EscrowMetadata{
		EscrowID: "escrow-1", CreationEpoch: 14, Model: "model-a",
		Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "participant-0"}},
	}, types.SessionConfig{}))
	require.ErrorContains(t, tracker.RecordCommittedDiff(
		"escrow-1", types.Diff{Nonce: 2}, nil,
	), "expected 1, got 2")
	require.Zero(t, tracker.Book.ReducedThrough("escrow-1"))
	require.NoError(t, tracker.RecordCommittedDiff("escrow-1", types.Diff{Nonce: 1}, nil))
	require.NoError(t, tracker.RecordCommittedDiff("escrow-1", types.Diff{Nonce: 2}, nil))
	require.Equal(t, uint64(2), tracker.Book.ReducedThrough("escrow-1"))
}

func TestDeadlineHeapOrderingUpdateAndCancel(t *testing.T) {
	now := time.Unix(2_000, 0)
	var due []deadlineKey
	scheduler := newDeadlineScheduler(func(entry *deadlineEntry) {
		due = append(due, entry.key)
	})
	scheduler.now = func() time.Time { return now }

	scheduler.upsert("escrow", 1, now.Add(10*time.Second), TimeoutRefused, PhaseNormal)
	scheduler.upsert("escrow", 2, now.Add(20*time.Second), TimeoutRefused, PhaseNormal)
	scheduler.upsert("escrow", 2, now.Add(5*time.Second), TimeoutExecution, PhasePoC)
	scheduler.upsert("escrow", 3, now.Add(7*time.Second), TimeoutRefused, PhaseNormal)
	scheduler.cancel("escrow", 3)

	scheduler.fireDue(now.Add(6 * time.Second))
	require.Equal(t, []deadlineKey{{escrowID: "escrow", nonce: 2}}, due)
	scheduler.fireDue(now.Add(20 * time.Second))
	require.Equal(t, []deadlineKey{
		{escrowID: "escrow", nonce: 2},
		{escrowID: "escrow", nonce: 1},
	}, due)
}

func TestSkippedTimeoutOutcomeAppliesAtDeadline(t *testing.T) {
	now := time.Unix(3_000, 0)
	tracker := newTestTracker(now)
	tracker.SetPhaseProvider(func() Phase { return PhasePoC })
	require.NoError(t, tracker.RegisterEscrow(EscrowMetadata{
		EscrowID: "escrow-1", CreationEpoch: 15, Model: "model-a",
		Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "participant-0"}},
	}, types.SessionConfig{RefusalTimeout: 10, ExecutionTimeout: 20}))
	start := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{
		StartInference: &types.MsgStartInference{InferenceId: 1},
	}}
	require.NoError(t, tracker.RecordCommittedDiff(
		"escrow-1", types.Diff{Nonce: 1, Txs: []*types.DevshardTx{start}}, nil,
	))
	require.NoError(t, tracker.RecordRealSend("escrow-1", 1, PhaseNormal, QuarantineNone))
	require.NoError(t, tracker.RecordTimeoutOutcome(TimeoutRecord{
		EscrowID: "escrow-1",
		Nonce:    1,
		Kind:     TimeoutRefused,
		Phase:    PhaseNormal,
		Outcome:  TimeoutSkipped,
		Reason:   TimeoutNotApplied,
	}))

	record := participantRecord(t, tracker.Book.Query(QueryFilter{EpochIndex: 15}), "participant-0")
	require.Empty(t, record.TimeoutOutcomes)

	tracker.deadlines.fireDue(now.Add(10 * time.Second))
	record = participantRecord(t, tracker.Book.Query(QueryFilter{EpochIndex: 15}), "participant-0")
	require.Equal(t, uint64(1), record.TimeoutOutcomes[TimeoutSkipped])
	require.Equal(t, uint64(1), record.Dispositions[DispositionUnfinishedRefused])
	require.Equal(t, PhasePoC, record.Counters[0].TimeoutEvaluationPhase)
}

func TestStaleRefusalDeadlineCannotBeatReceiptReplacement(t *testing.T) {
	now := time.Unix(4_000, 0)
	tracker := newTestTracker(now)
	require.NoError(t, tracker.RegisterEscrow(EscrowMetadata{
		EscrowID: "escrow-1", CreationEpoch: 17, Model: "model-a",
		Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "participant-0"}},
	}, types.SessionConfig{RefusalTimeout: 10, ExecutionTimeout: 20}))
	start := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{
		StartInference: &types.MsgStartInference{InferenceId: 1},
	}}
	require.NoError(t, tracker.RecordCommittedDiff(
		"escrow-1", types.Diff{Nonce: 1, Txs: []*types.DevshardTx{start}}, nil,
	))
	require.NoError(t, tracker.RecordRealSend("escrow-1", 1, PhaseNormal, QuarantineNone))

	key := deadlineKey{escrowID: "escrow-1", nonce: 1}
	stale := tracker.deadlines.entries[key]
	tracker.deadlines.mu.Lock()
	heap.Remove(&tracker.deadlines.queue, stale.heapIndex)
	tracker.deadlines.mu.Unlock()

	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{
		ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ConfirmedAt: now.Unix()},
	}}
	require.NoError(t, tracker.RecordCommittedDiff(
		"escrow-1", types.Diff{Nonce: 2, Txs: []*types.DevshardTx{confirm}}, nil,
	))
	tracker.onDeadline(stale)

	record := participantRecord(t, tracker.Book.Query(QueryFilter{EpochIndex: 17}), "participant-0")
	require.Zero(t, record.Dispositions[DispositionUnfinishedRefused])
	require.Zero(t, record.Dispositions[DispositionUnfinishedExecution])
	current := tracker.deadlines.entries[key]
	require.NotNil(t, current)
	require.Equal(t, TimeoutExecution, current.kind)
	require.Equal(t, now.Add(20*time.Second), current.at)
}

func TestReceiptRetargetsDeferredSkipToExecutionDeadline(t *testing.T) {
	now := time.Unix(5_000, 0)
	tracker := newTestTracker(now)
	require.NoError(t, tracker.RegisterEscrow(EscrowMetadata{
		EscrowID: "escrow-1", CreationEpoch: 18, Model: "model-a",
		Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "participant-0"}},
	}, types.SessionConfig{RefusalTimeout: 10, ExecutionTimeout: 20}))
	start := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{
		StartInference: &types.MsgStartInference{InferenceId: 1},
	}}
	require.NoError(t, tracker.RecordCommittedDiff(
		"escrow-1", types.Diff{Nonce: 1, Txs: []*types.DevshardTx{start}}, nil,
	))
	require.NoError(t, tracker.RecordRealSend("escrow-1", 1, PhaseNormal, QuarantineNone))
	require.NoError(t, tracker.RecordTimeoutOutcome(TimeoutRecord{
		EscrowID: "escrow-1", Nonce: 1, Kind: TimeoutRefused,
		Phase: PhaseNormal, Outcome: TimeoutSkipped, Reason: TimeoutNotApplied,
	}))
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{
		ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ConfirmedAt: now.Unix()},
	}}
	require.NoError(t, tracker.RecordCommittedDiff(
		"escrow-1", types.Diff{Nonce: 2, Txs: []*types.DevshardTx{confirm}}, nil,
	))

	tracker.deadlines.fireDue(now.Add(10 * time.Second))
	record := participantRecord(t, tracker.Book.Query(QueryFilter{EpochIndex: 18}), "participant-0")
	require.Empty(t, record.TimeoutOutcomes)
	tracker.deadlines.fireDue(now.Add(20 * time.Second))
	record = participantRecord(t, tracker.Book.Query(QueryFilter{EpochIndex: 18}), "participant-0")
	require.Equal(t, uint64(1), record.TimeoutOutcomes[TimeoutSkipped])
	require.Equal(t, uint64(1), record.Dispositions[DispositionUnfinishedExecution])
}

func TestFinalizedCommittedDiffCancelsEscrowDeadlines(t *testing.T) {
	now := time.Unix(6_000, 0)
	tracker := newTestTracker(now)
	require.NoError(t, tracker.RegisterEscrow(EscrowMetadata{
		EscrowID: "escrow-1", CreationEpoch: 19, Model: "model-a",
		Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "participant-0"}},
	}, types.SessionConfig{RefusalTimeout: 10, ExecutionTimeout: 20}))
	start := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{
		StartInference: &types.MsgStartInference{InferenceId: 1},
	}}
	require.NoError(t, tracker.RecordCommittedDiff(
		"escrow-1", types.Diff{Nonce: 1, Txs: []*types.DevshardTx{start}}, nil,
	))
	require.NoError(t, tracker.RecordRealSend("escrow-1", 1, PhaseNormal, QuarantineNone))
	require.NotEmpty(t, tracker.deadlines.entries)

	require.NoError(t, tracker.RecordCommittedDiff(
		"escrow-1",
		types.Diff{Nonce: 2},
		testProtocolView{state: types.EscrowState{Phase: types.PhaseSettlement}},
	))
	require.Empty(t, tracker.deadlines.entries)
	record := participantRecord(t, tracker.Book.Query(QueryFilter{EpochIndex: 19}), "participant-0")
	require.Equal(t, EscrowFinalized, record.LatestNonces[0].Phase)
}

func TestRecoveredTailDefersTerminalPhaseUntilAfterReconciliation(t *testing.T) {
	tracker := newTestTracker(time.Unix(7_000, 0))
	require.NoError(t, tracker.RegisterEscrow(EscrowMetadata{
		EscrowID: "escrow-1", CreationEpoch: 20, Model: "model-a",
		Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "participant-0"}},
	}, types.SessionConfig{RefusalTimeout: 10, ExecutionTimeout: 20}))
	start := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{
		StartInference: &types.MsgStartInference{InferenceId: 1},
	}}
	require.NoError(t, tracker.RecordCommittedDiff(
		"escrow-1", types.Diff{Nonce: 1, Txs: []*types.DevshardTx{start}}, nil,
	))
	require.NoError(t, tracker.RecordRealSend("escrow-1", 1, PhaseNormal, QuarantineNone))
	require.NoError(t, tracker.RecordUsage("escrow-1", 1, UsageWinner))
	require.NoError(t, tracker.RecordTimeoutOutcome(TimeoutRecord{
		EscrowID: "escrow-1", Nonce: 1, Kind: TimeoutRefused,
		Phase: PhaseNormal, Outcome: TimeoutInsufficientVotes,
	}))

	terminalView := testProtocolView{state: types.EscrowState{Phase: types.PhaseSettlement}}
	require.NoError(t, tracker.RecordRecoveredDiff(
		"escrow-1", types.Diff{Nonce: 2}, terminalView,
	))
	require.Equal(t, 1, tracker.Book.LiveNonceCount(),
		"tail replay must not compact pending timeout state")

	finish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{
		FinishInference: &types.MsgFinishInference{InferenceId: 1},
	}}
	require.NoError(t, tracker.RecordRecoveredDiff(
		"escrow-1", types.Diff{Nonce: 3, Txs: []*types.DevshardTx{finish}}, terminalView,
	))
	require.NoError(t, tracker.RecordPhase("escrow-1", EscrowFinalized))
	record := participantRecord(t, tracker.Book.Query(QueryFilter{EpochIndex: 20}), "participant-0")
	require.Equal(t, uint64(1), record.Dispositions[DispositionFinishedUsed])
	require.Equal(t, EscrowFinalized, record.LatestNonces[0].Phase)
}

func TestPhaseMetadataAndDiagnosticRingAreRuntimeOnly(t *testing.T) {
	book := NewBook()
	book.SetBlockHeightProvider(func() int64 { return 123 })
	registerTestEscrow(t, book, "escrow-1", 16, "model-a", EscrowActive)
	require.NoError(t, book.Apply(EscrowPhaseChanged{
		EscrowID: "escrow-1", Phase: EscrowFinalizing,
	}))
	for nonce := uint64(1); nonce <= diagnosticRingSize+10; nonce++ {
		require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: nonce}))
	}

	record := participantRecord(t, book.Query(QueryFilter{EpochIndex: 16}), "participant-1")
	require.Equal(t, EscrowFinalizing, record.LatestNonces[0].Phase)
	require.Len(t, book.Diagnostics(), diagnosticRingSize)
	require.Equal(t, int64(123), book.Diagnostics()[0].BlockHeight)

	recovered, err := NewBookFromSnapshot(book.Snapshot())
	require.NoError(t, err)
	require.Empty(t, recovered.Diagnostics())
	recoveredRecord := participantRecord(t, recovered.Query(QueryFilter{EpochIndex: 16}), "participant-1")
	require.Equal(t, EscrowFinalizing, recoveredRecord.LatestNonces[0].Phase)
}
