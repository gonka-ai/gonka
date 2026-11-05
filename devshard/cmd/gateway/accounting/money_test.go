package accounting

import (
	"testing"

	"devshard/types"
)

// Test flow:
//  1. Open an escrow with one slot and observe host stats reporting a cost and a miss.
//  2. Query the book.
//  3. Assert the single record's chain cost and chain-missed tallies match what was observed.
func TestSlotRecordCarriesWhatTheChainChargedTheSlot(t *testing.T) {
	t.Parallel()
	book := NewBook(nil)
	if err := book.OpenEscrow(EscrowMetadata{
		EscrowID: "5",
		Model:    "Qwen/Test",
		Slots:    []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "gonka1aaa"}},
	}); err != nil {
		t.Fatalf("OpenEscrow: %v", err)
	}
	if err := book.ObserveHostStats("5", 0, types.HostStats{Cost: 4_200, Missed: 1}); err != nil {
		t.Fatalf("ObserveHostStats: %v", err)
	}

	records := book.Query(QueryFilter{})

	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if got := records[0].ChainCost; got != 4_200 {
		t.Errorf("chain cost = %d, want 4200 -- the number the chain already told us", got)
	}
	if got := records[0].ChainMissed; got != 1 {
		t.Errorf("chain missed = %d, want 1 (the neighbouring counters must keep working)", got)
	}
}

// Test flow:
//  1. Open an escrow with one slot and observe a finished inference record with reserved and actual cost and token counts.
//  2. Record the attempt's output as produced.
//  3. Query the totals.
//  4. Assert reserved, actual and refunded costs and input/output tokens match the record.
func TestNonceCostsAreCarriedFromTheEscrowRecord(t *testing.T) {
	t.Parallel()
	book := NewBook(nil)
	if err := book.OpenEscrow(EscrowMetadata{EscrowID: "5", Model: "Qwen/Test", Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "gonka1aaa"}}}); err != nil {
		t.Fatalf("OpenEscrow: %v", err)
	}

	if err := book.ObserveInferences("5", map[uint64]*types.InferenceRecord{7: {
		ReservedCost: 10_000,
		ActualCost:   6_400,
		Status:       types.StatusFinished,
		InputTokens:  512,
		OutputTokens: 128,
	}}); err != nil {
		t.Fatalf("ObserveInferences: %v", err)
	}
	recordProduced(t, book, "5", 7, 128)

	totals := queryTotals(t, book)
	if got := totals.ReservedCost; got != 10_000 {
		t.Errorf("reserved = %d, want 10000", got)
	}
	if got := totals.ActualCost; got != 6_400 {
		t.Errorf("actual = %d, want 6400", got)
	}
	if got := totals.RefundedCost; got != 3_600 {
		t.Errorf("refunded = %d, want 3600 (reserved minus actual, never stored)", got)
	}
	if got := totals.InputTokens; got != 512 {
		t.Errorf("input tokens = %d, want 512", got)
	}
	if got := totals.OutputTokens; got != 128 {
		t.Errorf("output tokens = %d, want the 128 the gateway counted", got)
	}
}

// Test flow:
//  1. For each of the table's two statuses, pending and started, observe an inference record reserving cost but not finished.
//  2. Query the totals.
//  3. Assert the reserved cost is carried but the refunded cost is 0 while the nonce is still open.
func TestAnUnfinishedNonceRefundsNothing(t *testing.T) {
	t.Parallel()
	for _, status := range []types.InferenceStatus{types.StatusPending, types.StatusStarted} {
		t.Run(statusName(status), func(t *testing.T) {
			t.Parallel()
			book := NewBook(nil)
			if err := book.OpenEscrow(EscrowMetadata{EscrowID: "5", Model: "Qwen/Test", Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "gonka1aaa"}}}); err != nil {
				t.Fatalf("OpenEscrow: %v", err)
			}

			if err := book.ObserveInferences("5", map[uint64]*types.InferenceRecord{
				7: {ReservedCost: 10_000, Status: status},
			}); err != nil {
				t.Fatalf("ObserveInferences: %v", err)
			}

			totals := queryTotals(t, book)
			if got := totals.ReservedCost; got != 10_000 {
				t.Errorf("reserved = %d, want 10000", got)
			}
			if got := totals.RefundedCost; got != 0 {
				t.Errorf("refunded = %d, want 0 while the nonce is still open", got)
			}
		})
	}
}

func statusName(status types.InferenceStatus) string {
	if status == types.StatusPending {
		return "pending"
	}
	return "started"
}

// Test flow:
//  1. Open an escrow with two slots and observe the same finished cost record on two nonces of slot 1.
//  2. Record both nonces' output as produced.
//  3. Query the totals for the slot-1 participant.
//  4. Assert reserved, actual, refunded costs and input/output tokens are summed across the two nonces.
func TestMoneyIsSummedAcrossTheNoncesOfOneSlot(t *testing.T) {
	t.Parallel()
	book := NewBook(nil)
	if err := book.OpenEscrow(EscrowMetadata{
		EscrowID: "5", Model: "Qwen/Test",
		Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "gonka1aaa"}, {SlotID: 1, ValidatorAddress: "gonka1bbb"}},
	}); err != nil {
		t.Fatalf("OpenEscrow: %v", err)
	}

	spent := &types.InferenceRecord{
		ReservedCost: 1_000, ActualCost: 400, InputTokens: 10, OutputTokens: 4,
		Status: types.StatusFinished,
	}
	if err := book.ObserveInferences("5", map[uint64]*types.InferenceRecord{7: spent, 9: spent}); err != nil {
		t.Fatalf("ObserveInferences: %v", err)
	}
	recordProduced(t, book, "5", 7, 4)
	recordProduced(t, book, "5", 9, 4)

	var totals nonceTotals
	for _, record := range book.Query(QueryFilter{}) {
		if record.Participant == "gonka1bbb" {
			totals = record.nonceTotals
		}
	}
	for _, field := range []struct {
		name string
		got  uint64
		want uint64
	}{
		{"reserved", totals.ReservedCost, 2_000},
		{"actual", totals.ActualCost, 800},
		{"refunded", totals.RefundedCost, 1_200},
		{"input tokens", totals.InputTokens, 20},
		{"output tokens", totals.OutputTokens, 8},
	} {
		if field.got != field.want {
			t.Errorf("%s = %d, want %d across two nonces of one slot", field.name, field.got, field.want)
		}
	}
}

// Test flow:
//  1. Open an escrow with two slots owned by the same validator and observe the same finished cost record on one nonce per slot.
//  2. Record both nonces' output as produced and observe host-stats cost on each slot.
//  3. Query the totals.
//  4. Assert reserved, actual, refunded costs, input/output tokens and chain cost are summed across the two slots.
func TestMoneyIsSummedAcrossTheSlotsOfOneParticipant(t *testing.T) {
	t.Parallel()
	book := NewBook(nil)
	if err := book.OpenEscrow(EscrowMetadata{
		EscrowID: "5", Model: "Qwen/Test",
		Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "gonka1aaa"}, {SlotID: 1, ValidatorAddress: "gonka1aaa"}},
	}); err != nil {
		t.Fatalf("OpenEscrow: %v", err)
	}

	spent := &types.InferenceRecord{
		ReservedCost: 1_000, ActualCost: 400, InputTokens: 10, OutputTokens: 4, Status: types.StatusFinished,
	}
	if err := book.ObserveInferences("5", map[uint64]*types.InferenceRecord{7: spent, 8: spent}); err != nil {
		t.Fatalf("ObserveInferences: %v", err)
	}
	recordProduced(t, book, "5", 7, 4)
	recordProduced(t, book, "5", 8, 4)

	for slotID, cost := range map[uint32]uint64{0: 30, 1: 70} {
		if err := book.ObserveHostStats("5", slotID, types.HostStats{Cost: cost}); err != nil {
			t.Fatalf("ObserveHostStats %d: %v", slotID, err)
		}
	}

	totals := queryTotals(t, book)
	for _, field := range []struct {
		name string
		got  uint64
		want uint64
	}{
		{"reserved", totals.ReservedCost, 2_000},
		{"actual", totals.ActualCost, 800},
		{"refunded", totals.RefundedCost, 1_200},
		{"input tokens", totals.InputTokens, 20},
		{"output tokens", totals.OutputTokens, 8},
		{"chain cost", totals.ChainCost, 100},
	} {
		if field.got != field.want {
			t.Errorf("%s = %d, want %d across the two slots one validator owns", field.name, field.got, field.want)
		}
	}
}

// Test flow:
//  1. Observe an inference record with reserved and actual cost whose status is invalidated.
//  2. Query the totals.
//  3. Assert the refunded cost equals the whole reserve, not just the surplus.
func TestInvalidationRefundsTheWholeReserve(t *testing.T) {
	t.Parallel()
	book := NewBook(nil)
	if err := book.OpenEscrow(EscrowMetadata{EscrowID: "5", Model: "Qwen/Test", Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "gonka1aaa"}}}); err != nil {
		t.Fatalf("OpenEscrow: %v", err)
	}

	if err := book.ObserveInferences("5", map[uint64]*types.InferenceRecord{
		7: {ReservedCost: 1_000, ActualCost: 400, Status: types.StatusInvalidated},
	}); err != nil {
		t.Fatalf("ObserveInferences: %v", err)
	}

	if got := queryTotals(t, book).RefundedCost; got != 1_000 {
		t.Errorf("refunded = %d, want the whole reserve: invalidation returns the cost on top of the surplus", got)
	}
}

// Test flow:
//  1. Observe the same finished cost record for one nonce and record its output three times, as three sweeps would.
//  2. Query the totals.
//  3. Assert the reserved cost and output tokens stay at their single-sweep values rather than tripling.
func TestObservingTheSameNonceTwiceDoesNotDoubleTheMoney(t *testing.T) {
	t.Parallel()
	book := NewBook(nil)
	if err := book.OpenEscrow(EscrowMetadata{EscrowID: "5", Model: "Qwen/Test", Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "gonka1aaa"}}}); err != nil {
		t.Fatalf("OpenEscrow: %v", err)
	}
	record := &types.InferenceRecord{ReservedCost: 10_000, ActualCost: 6_400, OutputTokens: 128, Status: types.StatusFinished}

	for range 3 {
		if err := book.ObserveInferences("5", map[uint64]*types.InferenceRecord{7: record}); err != nil {
			t.Fatalf("ObserveInferences: %v", err)
		}
		recordProduced(t, book, "5", 7, 128)
	}

	totals := queryTotals(t, book)
	if got := totals.ReservedCost; got != 10_000 {
		t.Errorf("reserved = %d, want 10000 after three sweeps of one nonce", got)
	}
	if got := totals.OutputTokens; got != 128 {
		t.Errorf("output tokens = %d, want 128 after three sweeps and three reports of the same attempt", got)
	}
}

// recordProduced is the gateway reporting what an attempt streamed, which is where output tokens come from.
func recordProduced(t *testing.T, book *Book, escrowID string, nonce uint64, tokens int64) {
	t.Helper()
	if err := book.RecordRace(escrowID, []Attempt{{
		Nonce: nonce, RequestID: "request-1", Sent: true, Usage: UsageWinner, OutputTokens: tokens,
	}}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	markFinished(t, book, escrowID, nonce)
}

func queryTotals(t *testing.T, book *Book) nonceTotals {
	t.Helper()
	records := book.Query(QueryFilter{})
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	return records[0].nonceTotals
}

// Test flow:
//  1. Observe an inference record with input length, max tokens and input tokens.
//  2. Query the totals.
//  3. Assert estimated input, max tokens and input tokens carry the gateway's own estimate and the chain's own count.
func TestWhatTheHostWasGivenIsCarriedFromTheEscrowRecord(t *testing.T) {
	t.Parallel()
	book := NewBook(nil)
	if err := book.OpenEscrow(EscrowMetadata{EscrowID: "5", Model: "Qwen/Test", Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "gonka1aaa"}}}); err != nil {
		t.Fatalf("OpenEscrow: %v", err)
	}

	if err := book.ObserveInferences("5", map[uint64]*types.InferenceRecord{7: {
		InputLength:  2_048,
		MaxTokens:    256,
		InputTokens:  512,
		OutputTokens: 128,
		Status:       types.StatusFinished,
	}}); err != nil {
		t.Fatalf("ObserveInferences: %v", err)
	}

	totals := queryTotals(t, book)
	if got := totals.EstimatedInput; got != 512 {
		t.Errorf("estimated input = %d, want the gateway's own estimate of the 2048 bytes it handed over", got)
	}
	if got := totals.MaxTokens; got != 256 {
		t.Errorf("max tokens = %d, want the 256 the gateway reserved on the output side", got)
	}
	if got := totals.InputTokens; got != 512 {
		t.Errorf("input tokens = %d, want the chain's own count beside the bytes", got)
	}
}

// Test flow:
//  1. Record a ghost on nonce 7, then observe inference records for both the burned nonce 7 and a served, finished nonce 8.
//  2. Record nonce 8's output as produced.
//  3. Query the totals.
//  4. Assert estimated input and max tokens come only from the served nonce, not the burn's own prompt.
//  5. Assert reserved and refunded costs include the burn's reserve, and output tokens come only from the served nonce.
func TestABurnedNonceLeavesOutWhatItWasGivenAndNothingElse(t *testing.T) {
	t.Parallel()
	book := NewBook(nil)
	if err := book.OpenEscrow(EscrowMetadata{EscrowID: "5", Model: "Qwen/Test", Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "gonka1aaa"}}}); err != nil {
		t.Fatalf("OpenEscrow: %v", err)
	}
	if err := book.RecordGhost("5", 7, "participant_window_full_no_send"); err != nil {
		t.Fatalf("RecordGhost: %v", err)
	}

	if err := book.ObserveInferences("5", map[uint64]*types.InferenceRecord{
		7: {InputLength: 60, MaxTokens: 64, ReservedCost: 124, Status: types.StatusTimedOut},
		8: {
			InputLength: 2_048, MaxTokens: 256, ReservedCost: 2_304, ActualCost: 1_000,
			InputTokens: 512, OutputTokens: 128, Status: types.StatusFinished,
		},
	}); err != nil {
		t.Fatalf("ObserveInferences: %v", err)
	}
	recordProduced(t, book, "5", 8, 128)

	totals := queryTotals(t, book)
	for _, field := range []struct {
		name string
		got  uint64
		want uint64
		why  string
	}{
		{"estimated input", totals.EstimatedInput, 512, "the burn's own prompt is not a request's"},
		{"max tokens", totals.MaxTokens, 256, "the burn's 64 tokens were reserved by the gateway, not by a host"},
		{"reserved", totals.ReservedCost, 2_428, "money still counts the burn: a burn costs the escrow"},
		{"refunded", totals.RefundedCost, 1_428, "the burn's whole reserve came back, the served nonce's surplus with it"},
		{"output tokens", totals.OutputTokens, 128, "the burn produced nothing to add"},
	} {
		if field.got != field.want {
			t.Errorf("%s = %d, want %d -- %s", field.name, field.got, field.want, field.why)
		}
	}
}
