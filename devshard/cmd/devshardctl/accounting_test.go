package main

import (
	"path/filepath"
	"testing"
	"time"

	"devshard/accounting"
	"devshard/types"
	"devshard/user"

	"github.com/stretchr/testify/require"
)

func (r *gatewayAccountingRecorder) apply(event accounting.Event) {
	_ = r.service.Book.Apply(event)
}

func newAccountingRecorderForTest(
	t *testing.T,
	slots []types.SlotAssignment,
) (*accounting.Book, *gatewayAccountingRecorder) {
	t.Helper()
	service, err := accounting.OpenService(
		filepath.Join(t.TempDir(), "accounting.db"),
		0,
		time.Hour,
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Close()) })
	require.NoError(t, service.RegisterEscrow(accounting.EscrowMetadata{
		EscrowID:      "escrow-1",
		CreationEpoch: 7,
		Model:         "model-a",
		Phase:         accounting.EscrowActive,
		Slots:         slots,
	}, types.SessionConfig{RefusalTimeout: 60, ExecutionTimeout: 60}))
	return service.Book, newGatewayAccountingRecorder(service)
}

func TestGatewayAccountingRecorderMapsGatewayEvents(t *testing.T) {
	book, recorder := newAccountingRecorderForTest(t, []types.SlotAssignment{
		{SlotID: 0, ValidatorAddress: "participant-0"},
		{SlotID: 1, ValidatorAddress: "participant-1"},
	})

	recorder.apply(accounting.DiffApplied{EscrowID: "escrow-1", Nonce: 1, Inference: true})
	recorder.recordGhost("escrow-1", 1, "participant_throttled_no_send", "probe")
	recorder.apply(accounting.DiffApplied{EscrowID: "escrow-1", Nonce: 2, Inference: true})
	recorder.recordRealSend("escrow-1", 2, "shadow", time.Time{})
	recorder.recordTimeout("escrow-1", 2, "refused", "started", "none", "no_receipt", "none")
	pending := book.Query(accounting.QueryFilter{EpochIndex: 7})
	var pendingTimeouts uint64
	for _, record := range pending {
		for _, counter := range record.Counters {
			if counter.Disposition == accounting.DispositionUnfinishedRefused &&
				counter.TimeoutOutcome == "" {
				pendingTimeouts += counter.Count
			}
		}
	}
	require.Zero(t, pendingTimeouts)
	recorder.recordTimeout("escrow-1", 2, "refused", "failed", "insufficient_votes", "no_receipt", "unknown")

	records := book.Query(accounting.QueryFilter{EpochIndex: 7})
	require.Len(t, records, 2)
	var dispositions = make(map[accounting.Disposition]uint64)
	var outcomes = make(map[accounting.TimeoutOutcome]uint64)
	for _, record := range records {
		for disposition, count := range record.Dispositions {
			dispositions[disposition] += count
		}
		for outcome, count := range record.TimeoutOutcomes {
			outcomes[outcome] += count
		}
	}
	require.Equal(t, uint64(1), dispositions[accounting.DispositionGhost])
	require.Equal(t, uint64(1), dispositions[accounting.DispositionUnfinishedRefused])
	require.Equal(t, uint64(1), outcomes[accounting.TimeoutInsufficientVotes])
	for _, record := range records {
		for _, counter := range record.Counters {
			if counter.Disposition == accounting.DispositionUnfinishedRefused {
				require.Equal(t, accounting.FailureTransportUnknown, counter.FailureOrigin)
			}
		}
	}
}

func TestGatewayAccountingRecorderIgnoresFinishedTimeoutRace(t *testing.T) {
	book, recorder := newAccountingRecorderForTest(t, []types.SlotAssignment{
		{SlotID: 0, ValidatorAddress: "participant-0"},
	})
	recorder.apply(accounting.DiffApplied{EscrowID: "escrow-1", Nonce: 1, Inference: true})
	recorder.recordRealSend("escrow-1", 1, "none", time.Time{})
	recorder.apply(accounting.ProtocolTransition{
		EscrowID: "escrow-1",
		Nonce:    1,
		Kind:     accounting.ProtocolFinishApplied,
	})
	recorder.recordUsage("escrow-1", 1, 1)
	recorder.recordTimeout("escrow-1", 1, "execution", "skipped", "nonce_already_finished", "", "nonce_already_finished")

	record := book.Query(accounting.QueryFilter{EpochIndex: 7})[0]
	require.Equal(t, uint64(1), record.Dispositions[accounting.DispositionFinishedUsed])
	require.Empty(t, record.TimeoutOutcomes)
}

// unknown_reason_total must mark only the cases where the gateway could not
// name a cause. A dead host that never sent a receipt, and votes that fell
// short, are both fully explained by the dimensions already recorded.
func TestGatewayAccountingUnknownReasonMarksOnlyRealGaps(t *testing.T) {
	newBook := func(t *testing.T) (*accounting.Book, *gatewayAccountingRecorder) {
		t.Helper()
		return newAccountingRecorderForTest(t, []types.SlotAssignment{
			{SlotID: 0, ValidatorAddress: "participant-0"},
		})
	}
	unknownTotal := func(book *accounting.Book) uint64 {
		var total uint64
		for _, record := range book.Query(accounting.QueryFilter{EpochIndex: 7}) {
			total += record.UnknownReasonTotal
		}
		return total
	}

	t.Run("applied timeout without a receipt", func(t *testing.T) {
		book, recorder := newBook(t)
		recorder.apply(accounting.DiffApplied{EscrowID: "escrow-1", Nonce: 1, Inference: true})
		recorder.recordRealSend("escrow-1", 1, "none", time.Now().Add(-time.Hour))
		recorder.recordTimeout("escrow-1", 1, "refused", "completed", "none", "no_receipt", "none")
		require.Zero(t, unknownTotal(book))
	})

	t.Run("insufficient votes", func(t *testing.T) {
		book, recorder := newBook(t)
		recorder.apply(accounting.DiffApplied{EscrowID: "escrow-1", Nonce: 1, Inference: true})
		recorder.recordRealSend("escrow-1", 1, "none", time.Time{})
		recorder.recordTimeout("escrow-1", 1, "refused", "failed", "insufficient_votes", "not_finished", "")
		require.Zero(t, unknownTotal(book))
	})

	t.Run("ghost with an unlisted policy", func(t *testing.T) {
		book, recorder := newBook(t)
		recorder.apply(accounting.DiffApplied{EscrowID: "escrow-1", Nonce: 1, Inference: true})
		recorder.recordGhost("escrow-1", 1, "brand_new_policy", "none")
		require.Equal(t, uint64(1), unknownTotal(book))
	})

	t.Run("skip with an unlisted reason", func(t *testing.T) {
		book, recorder := newBook(t)
		recorder.apply(accounting.DiffApplied{EscrowID: "escrow-1", Nonce: 1, Inference: true})
		recorder.recordRealSend("escrow-1", 1, "none", time.Now().Add(-time.Hour))
		recorder.recordTimeout("escrow-1", 1, "refused", "skipped", "brand_new_skip", "", "brand_new_skip")
		require.Equal(t, uint64(1), unknownTotal(book))
	})
}

func TestGatewayTimeoutFailureActionPreservesLocalApplication(t *testing.T) {
	action, reason := gatewayTimeoutFailureAction(user.TimeoutResult{
		Outcome:      "applied",
		DetailReason: "timeout_diff_delivery_failed",
		Applied:      true,
	})
	require.Equal(t, "completed", action)
	require.Equal(t, "timeout_diff_delivery_failed", reason)
}

func TestProtocolSettlementPhaseIsOnlyFinalized(t *testing.T) {
	require.Equal(t, accounting.EscrowFinalized, accountingEscrowPhase(types.PhaseSettlement))
}
