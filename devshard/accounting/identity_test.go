package accounting

import (
	"testing"

	"devshard/types"

	"github.com/stretchr/testify/require"
)

// credit holds the derived view from proposals/gateway-dashboard/README.md.
// The API returns raw counts only, so the derivation lives here.
type credit struct {
	assigned                 uint64
	protocolMisses           uint64
	executed                 uint64
	protocolCompleted        uint64
	nonExecutionCredit       uint64
	protocolOnly             uint64
	ghost                    uint64
	timeoutAccountingFailure uint64
	timeoutPending           uint64
	unresolved               uint64
}

func deriveCredit(record ParticipantRecord) credit {
	unfinished := record.Dispositions[DispositionUnfinishedRefused] +
		record.Dispositions[DispositionUnfinishedExecution]
	var outcomes uint64
	for _, count := range record.TimeoutOutcomes {
		outcomes += count
	}
	c := credit{
		assigned:       record.AssignedNonces,
		protocolMisses: record.ProtocolMisses,
		executed: record.Dispositions[DispositionFinishedUsed] +
			record.Dispositions[DispositionFinishedUnused] +
			record.Dispositions[DispositionFinishedUsageUnknown],
		protocolOnly:             record.Dispositions[DispositionProtocolOnly],
		ghost:                    record.Dispositions[DispositionGhost],
		timeoutAccountingFailure: outcomes - record.TimeoutOutcomes[TimeoutApplied],
		timeoutPending:           unfinished - outcomes,
	}
	c.protocolCompleted = c.assigned - c.protocolMisses
	c.nonExecutionCredit = c.protocolCompleted - c.executed
	c.unresolved = record.InFlight + c.timeoutPending + record.Unclassified
	return c
}

func sumCredit(records []ParticipantRecord) credit {
	var total credit
	for _, record := range records {
		part := deriveCredit(record)
		total.assigned += part.assigned
		total.protocolMisses += part.protocolMisses
		total.executed += part.executed
		total.protocolCompleted += part.protocolCompleted
		total.nonExecutionCredit += part.nonExecutionCredit
		total.protocolOnly += part.protocolOnly
		total.ghost += part.ghost
		total.timeoutAccountingFailure += part.timeoutAccountingFailure
		total.timeoutPending += part.timeoutPending
		total.unresolved += part.unresolved
	}
	return total
}

// TestNonExecutionCreditIdentity drives one escrow through every bucket the
// proposal defines and checks that
// non_execution_credit = protocol_only + ghost + timeout_accounting_failure +
// unresolved, where unresolved = in_flight + timeout_pending + unclassified.
// The identity holds exactly while timeout_outcome.applied == HostStats.Missed.
func TestNonExecutionCreditIdentity(t *testing.T) {
	book := NewBook()
	require.NoError(t, book.Apply(EscrowRegistered{Metadata: EscrowMetadata{
		EscrowID:      "escrow-1",
		CreationEpoch: 7,
		Model:         "model-a",
		Phase:         EscrowActive,
		Slots: []types.SlotAssignment{
			{SlotID: 0, ValidatorAddress: "participant-0"},
			{SlotID: 1, ValidatorAddress: "participant-1"},
			{SlotID: 2, ValidatorAddress: "participant-2"},
			{SlotID: 3, ValidatorAddress: "participant-3"},
		},
	}}))

	// nonce 1: ghost.
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 1, Inference: true}))
	require.NoError(t, book.Apply(Ghost{
		EscrowID: "escrow-1",
		Nonce:    1,
		Reason:   NoSendPoCUnavailable,
	}))

	// nonce 2: protocol-only carrier diff.
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 2}))

	// nonce 3: executed and used. nonce 4: executed, overscheduled loser.
	for nonce, usage := range map[uint64]Event{
		3: Winner{EscrowID: "escrow-1", Nonce: 3},
		4: Loser{EscrowID: "escrow-1", Nonce: 4},
	} {
		require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: nonce, Inference: true}))
		require.NoError(t, book.Apply(RealSend{EscrowID: "escrow-1", Nonce: nonce}))
		require.NoError(t, book.Apply(ProtocolTransition{
			EscrowID: "escrow-1", Nonce: nonce, Kind: ProtocolFinishApplied,
		}))
		require.NoError(t, book.Apply(usage))
	}

	// nonce 5: required timeout that applied, matched by HostStats.Missed.
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 5, Inference: true}))
	require.NoError(t, book.Apply(RealSend{EscrowID: "escrow-1", Nonce: 5}))
	require.NoError(t, book.Apply(ProtocolTransition{
		EscrowID: "escrow-1", Nonce: 5, Kind: ProtocolReceiptApplied,
	}))
	require.NoError(t, book.Apply(TimeoutRequired{
		EscrowID: "escrow-1", Nonce: 5, Kind: TimeoutExecution, FailureOrigin: FailureHostResponse,
	}))
	require.NoError(t, book.Apply(TimeoutOutcomeRecorded{
		EscrowID: "escrow-1", Nonce: 5, Outcome: TimeoutApplied,
	}))
	require.NoError(t, book.Apply(HostStatsObserved{
		EscrowID: "escrow-1", SlotID: 1, Stats: types.HostStats{Missed: 1},
	}))

	// nonce 6: required timeout that failed to apply.
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 6, Inference: true}))
	require.NoError(t, book.Apply(RealSend{EscrowID: "escrow-1", Nonce: 6}))
	require.NoError(t, book.Apply(TimeoutRequired{
		EscrowID: "escrow-1", Nonce: 6, Kind: TimeoutRefused,
	}))
	require.NoError(t, book.Apply(TimeoutOutcomeRecorded{
		EscrowID: "escrow-1", Nonce: 6, Outcome: TimeoutInsufficientVotes,
	}))

	// nonce 7: required timeout still being handled.
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 7, Inference: true}))
	require.NoError(t, book.Apply(RealSend{EscrowID: "escrow-1", Nonce: 7}))
	require.NoError(t, book.Apply(TimeoutRequired{
		EscrowID: "escrow-1", Nonce: 7, Kind: TimeoutRefused,
	}))

	// nonce 8: sent, still before its deadline.
	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 8, Inference: true}))
	require.NoError(t, book.Apply(RealSend{EscrowID: "escrow-1", Nonce: 8}))

	// nonces 9 and 10 were consumed without any recorded event.
	require.NoError(t, book.Apply(LatestNonceObserved{EscrowID: "escrow-1", LatestNonce: 10}))

	records := book.Query(QueryFilter{EpochIndex: 7})
	total := sumCredit(records)

	require.Equal(t, uint64(10), total.assigned)
	require.Equal(t, uint64(1), total.protocolMisses)
	require.Equal(t, uint64(2), total.executed)
	require.Equal(t, uint64(9), total.protocolCompleted)
	require.Equal(t, uint64(7), total.nonExecutionCredit)

	require.Equal(t, uint64(1), total.protocolOnly)
	require.Equal(t, uint64(1), total.ghost)
	require.Equal(t, uint64(1), total.timeoutAccountingFailure)
	require.Equal(t, uint64(1), total.timeoutPending)
	require.Equal(t, uint64(4), total.unresolved)

	require.Equal(t,
		total.nonExecutionCredit,
		total.protocolOnly+total.ghost+total.timeoutAccountingFailure+total.unresolved,
		"non_execution_credit must split exactly")

	for _, record := range records {
		require.Zerof(t, record.CrossChecks.ErrorCount,
			"participant %s must have no cross-check error", record.Participant)
		identity := deriveCredit(record)
		require.Equal(t,
			identity.nonExecutionCredit,
			identity.protocolOnly+identity.ghost+identity.timeoutAccountingFailure+identity.unresolved,
			"the identity must hold per participant too")
	}
}

// TestNonExecutionCreditIdentityBreaksOnCrossCheckError pins the proposal's
// claim that the identity holds only while applied timeouts match
// HostStats.Missed.
func TestNonExecutionCreditIdentityBreaksOnCrossCheckError(t *testing.T) {
	book := NewBook()
	registerTestEscrow(t, book, "escrow-1", 7, "model-a", EscrowActive)

	require.NoError(t, book.Apply(DiffApplied{EscrowID: "escrow-1", Nonce: 1, Inference: true}))
	require.NoError(t, book.Apply(RealSend{EscrowID: "escrow-1", Nonce: 1}))
	require.NoError(t, book.Apply(TimeoutRequired{
		EscrowID: "escrow-1", Nonce: 1, Kind: TimeoutRefused,
	}))
	require.NoError(t, book.Apply(TimeoutOutcomeRecorded{
		EscrowID: "escrow-1", Nonce: 1, Outcome: TimeoutApplied,
	}))
	// The protocol never recorded the miss, so the projection is off by one.
	records := book.Query(QueryFilter{EpochIndex: 7})
	total := sumCredit(records)

	require.Equal(t, uint64(1), total.nonExecutionCredit)
	require.Zero(t, total.protocolMisses)
	var crossCheckErrors uint64
	for _, record := range records {
		crossCheckErrors += record.CrossChecks.ErrorCount
	}
	require.Equal(t, uint64(1), crossCheckErrors)
	require.NotEqual(t,
		total.nonExecutionCredit,
		total.protocolOnly+total.ghost+total.timeoutAccountingFailure+total.unresolved)
}
