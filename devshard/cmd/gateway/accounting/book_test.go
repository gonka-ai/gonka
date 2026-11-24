package accounting

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/types"
)

const (
	testEscrow = "escrow-1"
	testModel  = "model-a"
	testEpoch  = 7
)

func markFinished(t *testing.T, book *Book, escrowID string, nonces ...uint64) {
	t.Helper()
	if err := book.MarkFinished(escrowID, nonces); err != nil {
		t.Fatalf("MarkFinished(): %v", err)
	}
}

func newTestBook(t *testing.T, groupSize int) *Book {
	t.Helper()
	book := NewBook(func() time.Time { return time.Unix(0, 0).UTC() })
	openTestEscrow(t, book, testEscrow, testEpoch, groupSize)
	return book
}

// openTestEscrow gives every escrow the same slot-to-participant mapping.
func openTestEscrow(t *testing.T, book *Book, escrowID string, epoch uint64, groupSize int) {
	t.Helper()
	openModelEscrow(t, book, escrowID, testModel, epoch, groupSize, 0)
}

func openModelEscrow(t *testing.T, book *Book, escrowID, model string, epoch uint64, groupSize, firstParticipant int) {
	t.Helper()
	slots := make([]types.SlotAssignment, 0, groupSize)
	for slotID := range groupSize {
		slots = append(slots, types.SlotAssignment{
			SlotID:           uint32(slotID),
			ValidatorAddress: participantFor(firstParticipant + slotID),
		})
	}
	if err := book.OpenEscrow(EscrowMetadata{
		EscrowID: escrowID, Model: model, CreationEpoch: epoch, Slots: slots,
	}); err != nil {
		t.Fatalf("OpenEscrow(%s): %v", escrowID, err)
	}
}

func participantFor(slotID int) string {
	return string(rune('A' + slotID))
}

// challengedNonces is the escrow state a sweep reads when a slot has challenges still open against it.
func challengedNonces(slotID uint32, count uint64) map[uint64]*types.InferenceRecord {
	inferences := make(map[uint64]*types.InferenceRecord, count)
	for nonce := range count {
		inferences[nonce+1] = &types.InferenceRecord{Status: types.StatusChallenged, ExecutorSlot: slotID}
	}
	return inferences
}

// slotOfNonce mirrors the chain's convention in the test so an expectation names the slot it means
// rather than repeating the arithmetic under test.
func slotOfNonce(nonce uint64, groupSize int) uint32 { return uint32(nonce % uint64(groupSize)) }

func dispositionsOfSlot(t *testing.T, book *Book, slotID uint32) map[Disposition]uint64 {
	t.Helper()
	for _, record := range book.Query(QueryFilter{}) {
		for _, slot := range record.Slots {
			if slot.SlotID == slotID {
				return slot.Dispositions
			}
		}
	}
	t.Fatalf("slot %d absent from the ledger", slotID)
	return nil
}

func assertDisposition(t *testing.T, book *Book, nonce uint64, groupSize int, want Disposition) {
	t.Helper()
	dispositions := dispositionsOfSlot(t, book, slotOfNonce(nonce, groupSize))
	if dispositions[want] != 1 {
		t.Fatalf("dispositions = %v, want exactly one %s", dispositions, want)
	}
	var total uint64
	for _, count := range dispositions {
		total += count
	}
	if total != 1 {
		t.Fatalf("dispositions = %v, want one nonce classified once", dispositions)
	}
}

// Test flow:
//  1. Record a ghost on nonce 5 with reason "participant_window_full_no_send".
//  2. Assert the nonce's disposition is `DispositionGhost`.
//  3. Assert every ghost counter carries that same reason string.
func TestGhostIsCountedWithItsReason(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.RecordGhost(testEscrow, 5, "participant_window_full_no_send"); err != nil {
		t.Fatalf("RecordGhost(): %v", err)
	}
	assertDisposition(t, book, 5, 4, DispositionGhost)

	records := book.Query(QueryFilter{})
	for _, record := range records {
		for _, counter := range record.Counters {
			if counter.Disposition == DispositionGhost && counter.GhostReason != "participant_window_full_no_send" {
				t.Fatalf("ghost reason = %q, want the scheduler's own label", counter.GhostReason)
			}
		}
	}
}

// Test flow:
//  1. For each table case, record a finished race attempt with the case's usage: winner, loser or unknown.
//  2. Assert the nonce's disposition matches the case's expected disposition.
func TestFinishedNoncesSplitByWhoUsedTheAnswer(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		usage Usage
		want  Disposition
	}{
		{name: "winner", usage: UsageWinner, want: DispositionFinishedUsed},
		{name: "loser", usage: UsageLoser, want: DispositionFinishedUnused},
		{name: "unknown", usage: UsageUnknown, want: DispositionFinishedUsageUnknown},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			book := newTestBook(t, 4)
			attempt := Attempt{Nonce: 9, Sent: true, Usage: testCase.usage}
			if err := book.RecordRace(testEscrow, []Attempt{attempt}); err != nil {
				t.Fatalf("RecordRace(): %v", err)
			}
			markFinished(t, book, testEscrow, 9)
			assertDisposition(t, book, 9, 4, testCase.want)
		})
	}
}

// Test flow:
//  1. Record a race with one sent, unfinished attempt on nonce 6.
//  2. Assert the slot has no disposition yet and counts the nonce as pending.
//  3. Record a timeout for nonce 6 with outcome "completed".
//  4. Assert the disposition becomes `DispositionUnfinishedExecution` and pending drops to 0.
func TestUnfinishedNonceStaysPendingUntilItsTimeoutSettles(t *testing.T) {
	book := newTestBook(t, 4)
	attempt := Attempt{Nonce: 6, Sent: true}
	if err := book.RecordRace(testEscrow, []Attempt{attempt}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}

	slot := slotOfNonce(6, 4)
	if dispositions := dispositionsOfSlot(t, book, slot); len(dispositions) != 0 {
		t.Fatalf("dispositions = %v, want none while the timeout is unsettled", dispositions)
	}
	if pending := unclassifiedOfSlot(t, book, slot); pending != 1 {
		t.Fatalf("pending = %d, want the nonce awaiting its timeout", pending)
	}

	if err := book.RecordTimeout(testEscrow, 6, "execution", "completed", "none"); err != nil {
		t.Fatalf("RecordTimeout(): %v", err)
	}
	assertDisposition(t, book, 6, 4, DispositionUnfinishedExecution)
	if pending := unclassifiedOfSlot(t, book, slot); pending != 0 {
		t.Fatalf("pending = %d, want the nonce classified", pending)
	}
}

// Test flow:
//  1. Record a race with one sent, unfinished attempt on nonce 6.
//  2. Record a timeout for nonce 6 with kind `engine.TimeoutKindRefused`.
//  3. Assert the disposition is `DispositionUnfinishedRefused`.
func TestUnfinishedNonceThatWasNeverAcknowledgedCountsAsRefused(t *testing.T) {
	book := newTestBook(t, 4)
	attempt := Attempt{Nonce: 6, Sent: true}
	if err := book.RecordRace(testEscrow, []Attempt{attempt}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	if err := book.RecordTimeout(testEscrow, 6, engine.TimeoutKindRefused, "completed", "none"); err != nil {
		t.Fatalf("RecordTimeout(): %v", err)
	}
	assertDisposition(t, book, 6, 4, DispositionUnfinishedRefused)
}

// Test flow:
//  1. Record a race on nonce 8 with usage loser and assert the disposition is `DispositionFinishedUnused`.
//  2. Record the same nonce again, now with usage winner.
//  3. Assert the disposition moved to `DispositionFinishedUsed` rather than adding a second count.
func TestReclassificationMovesANonceRatherThanDuplicatingIt(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.RecordRace(testEscrow, []Attempt{{Nonce: 8, Sent: true, Usage: UsageLoser}}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	markFinished(t, book, testEscrow, 8)
	assertDisposition(t, book, 8, 4, DispositionFinishedUnused)

	if err := book.RecordRace(testEscrow, []Attempt{{Nonce: 8, Sent: true, Usage: UsageWinner}}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	markFinished(t, book, testEscrow, 8)
	assertDisposition(t, book, 8, 4, DispositionFinishedUsed)
}

// Test flow:
//  1. Record a ghost against an escrow id the book never opened.
//  2. Assert the call returns `ErrUnknownEscrow`.
func TestAFactForAnUnopenedEscrowIsRefused(t *testing.T) {
	book := newTestBook(t, 4)
	err := book.RecordGhost("escrow-unknown", 1, "poc_unavailable_host")
	if !errors.Is(err, ErrUnknownEscrow) {
		t.Fatalf("RecordGhost() = %v, want ErrUnknownEscrow", err)
	}
}

// Test flow:
//  1. For each table case of latest nonce, group size and slot id, call `assignedForSlot`.
//  2. Assert the result matches the case's expected assigned count, covering slot zero, an inner slot, the last slot, a slot beyond the latest nonce, and nothing spent yet.
func TestAssignedFollowsTheChainsModuloConvention(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		latest    uint64
		groupSize uint32
		slotID    uint32
		want      uint64
	}{
		{name: "slot zero takes the multiples", latest: 10, groupSize: 4, slotID: 0, want: 2},
		{name: "first slot", latest: 10, groupSize: 4, slotID: 1, want: 3},
		{name: "last slot", latest: 10, groupSize: 4, slotID: 3, want: 2},
		{name: "slot beyond the latest nonce", latest: 2, groupSize: 4, slotID: 3, want: 0},
		{name: "nothing spent", latest: 0, groupSize: 4, slotID: 1, want: 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := assignedForSlot(testCase.latest, testCase.groupSize, testCase.slotID); got != testCase.want {
				t.Fatalf("assignedForSlot(%d, %d, %d) = %d, want %d",
					testCase.latest, testCase.groupSize, testCase.slotID, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Open a book, observe the latest nonce as 12, record a ghost on nonce 5, and record a race with one finished and one unfinished attempt.
//  2. For each slot, sum classified dispositions, pending, in-flight and unobserved counts.
//  3. Assert that sum equals the slot's assigned count with nothing overcounted.
func TestEveryAssignedNonceIsAccountedForExactlyOnce(t *testing.T) {
	const groupSize = 4
	book := newTestBook(t, groupSize)
	if err := book.ObserveLatestNonce(testEscrow, 12); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	if err := book.RecordGhost(testEscrow, 5, "participant_window_full_no_send"); err != nil {
		t.Fatalf("RecordGhost(): %v", err)
	}
	if err := book.RecordRace(testEscrow, []Attempt{
		{Nonce: 9, Sent: true, Usage: UsageWinner},
		{Nonce: 6, Sent: true},
	}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	markFinished(t, book, testEscrow, 9)

	for _, record := range book.Query(QueryFilter{}) {
		for _, slot := range record.Slots {
			var classified uint64
			for _, count := range slot.Dispositions {
				classified += count
			}
			total := classified + slot.Pending + slot.InFlight + slot.Unobserved
			if slot.Overcounted != 0 {
				t.Fatalf("slot %d overcounted %d nonces", slot.SlotID, slot.Overcounted)
			}
			if total != slot.Assigned {
				t.Fatalf("slot %d: classified %d + pending %d + in flight %d + unobserved %d = %d, want assigned %d",
					slot.SlotID, classified, slot.Pending, slot.InFlight, slot.Unobserved, total, slot.Assigned)
			}
		}
	}
}

// Test flow:
//  1. Observe host stats for slot 2 reporting 3 misses and 1 invalid.
//  2. Find the record for slot 2's participant.
//  3. Assert its chain-missed and chain-invalid tallies match the observed 3 and 1.
func TestChainTalliesTravelBesideTheLedgersOwn(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveHostStats(testEscrow, 2, types.HostStats{Missed: 3, Invalid: 1}); err != nil {
		t.Fatalf("ObserveHostStats(): %v", err)
	}
	for _, record := range book.Query(QueryFilter{}) {
		if record.Participant != participantFor(2) {
			continue
		}
		if record.ChainMissed != 3 || record.ChainInvalid != 1 {
			t.Fatalf("chain tallies = missed %d, invalid %d; want 3 and 1", record.ChainMissed, record.ChainInvalid)
		}
	}
}

func unclassifiedOfSlot(t *testing.T, book *Book, slotID uint32) uint64 {
	t.Helper()
	for _, record := range book.Query(QueryFilter{}) {
		for _, slot := range record.Slots {
			if slot.SlotID == slotID {
				return slot.Pending + slot.InFlight
			}
		}
	}
	t.Fatalf("slot %d absent from the ledger", slotID)
	return 0
}

// Test flow:
//  1. Record a ghost against a freshly opened escrow.
//  2. Re-open the same escrow id with a later creation epoch.
//  3. Assert every record still reports the original epoch it was first seen in.
func TestReopeningAnEscrowKeepsTheEpochItWasFirstSeenIn(t *testing.T) {
	book := newTestBook(t, 2)
	if err := book.RecordGhost(testEscrow, 1, "participant_window_full_no_send"); err != nil {
		t.Fatalf("RecordGhost(): %v", err)
	}

	if err := book.OpenEscrow(EscrowMetadata{
		EscrowID: testEscrow, Model: testModel, CreationEpoch: testEpoch + 5,
		Slots: []types.SlotAssignment{
			{SlotID: 0, ValidatorAddress: participantFor(0)},
			{SlotID: 1, ValidatorAddress: participantFor(1)},
		},
	}); err != nil {
		t.Fatalf("OpenEscrow() on a known escrow: %v", err)
	}

	for _, record := range book.Query(QueryFilter{}) {
		if record.EpochIndex != testEpoch {
			t.Fatalf("epoch = %d, want the epoch of the first sighting", record.EpochIndex)
		}
	}
}

// Test flow:
//  1. Observe the latest nonce as 1 and record a race attempt carrying every race fact: terminal, phase, slow receipt, slow chunk and clock drift.
//  2. Assert the query returns one record holding exactly one counter bucket.
//  3. Assert that bucket's key carries every one of those race facts.
func TestRaceFactsReachTheCounters(t *testing.T) {
	book := newTestBook(t, 1)
	if err := book.ObserveLatestNonce(testEscrow, 1); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}

	if err := book.RecordRace(testEscrow, []Attempt{{
		Nonce: 1, Sent: true, Acknowledged: true, Usage: UsageWinner,
		Terminal: "won", Phase: PhasePoC, SlowReceipt: true, SlowChunk: true, ClockDrifted: true,
	}}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	markFinished(t, book, testEscrow, 1)

	records := book.Query(QueryFilter{})
	if len(records) != 1 || len(records[0].Counters) != 1 {
		t.Fatalf("got %d records, want one participant holding one bucket", len(records))
	}
	key := records[0].Counters[0].CounterKey
	if key.Terminal != "won" || key.Phase != PhasePoC ||
		!key.SlowReceipt || !key.SlowChunk || !key.ClockDrifted {
		t.Fatalf("counter key = %+v, want every race fact carried onto the bucket", key)
	}
}

// Test flow:
//  1. Observe the latest nonce as 1 and record a race attempt on nonce 1 with no terminal reported.
//  2. Mark nonce 1 finished directly, bypassing the race's own terminal report.
//  3. Assert the query returns exactly one counter bucket.
//  4. Assert that bucket's terminal is `TerminalUnreported` rather than blank.
func TestANonceWithoutRaceFactsStillNamesATerminal(t *testing.T) {
	book := newTestBook(t, 1)
	if err := book.ObserveLatestNonce(testEscrow, 1); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}
	if err := book.RecordRace(testEscrow, []Attempt{{Nonce: 1, Sent: true, Usage: UsageWinner}}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}

	if err := book.MarkFinished(testEscrow, []uint64{1}); err != nil {
		t.Fatalf("MarkFinished(): %v", err)
	}

	counters := book.Query(QueryFilter{})[0].Counters
	if len(counters) != 1 {
		t.Fatalf("got %d buckets, want one", len(counters))
	}
	if counters[0].Terminal != TerminalUnreported {
		t.Fatalf("terminal = %q, want it named %q", counters[0].Terminal, TerminalUnreported)
	}
}

// Test flow:
//  1. Build a `twoEpochBook` fixture and confirm the target epoch has records to clear.
//  2. Reset that epoch.
//  3. Assert the reset reports 1 escrow cleared.
//  4. Assert the target epoch's records are gone while the neighbouring epoch's records and escrow id remain.
func TestResetClearsOneEpochAndLeavesTheRest(t *testing.T) {
	book := twoEpochBook(t)
	if len(book.Query(QueryFilter{EpochIndex: testEpoch})) == 0 {
		t.Fatal("fixture produced no records to clear")
	}

	cleared := book.ResetEpoch(testEpoch)

	if cleared != 1 {
		t.Fatalf("cleared %d escrows, want 1: an operator is told what the reset took", cleared)
	}
	if records := book.Query(QueryFilter{EpochIndex: testEpoch}); len(records) != 0 {
		t.Fatalf("got %d records for the cleared epoch, want none", len(records))
	}
	if records := book.Query(QueryFilter{EpochIndex: testEpoch + 1}); len(records) == 0 {
		t.Fatal("the neighbouring epoch went with it: its escrows are still live and still counted")
	}
	if escrows := book.EscrowIDs(); !slices.Equal(escrows, []string{secondTestEscrow}) {
		t.Fatalf("escrows = %v, want only %s", escrows, secondTestEscrow)
	}
}

// Test flow:
//  1. Build a `Service` over a real store, open an escrow, and record a ghost against it.
//  2. Flush the ledger to the store before resetting.
//  3. Reset the epoch through the service.
//  4. Load the store's snapshot into a fresh book and restore it.
//  5. Assert the restored book has no records for the escrow, so the reset survives a restart.
func TestResetIsWrittenOutSoARestartCannotUndoIt(t *testing.T) {
	store := openTestStore(t)
	service, err := NewService(Settings{Store: store, Now: func() time.Time { return time.Unix(0, 0).UTC() }})
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	openTestEscrow(t, service.Book, testEscrow, testEpoch, 2)
	if err := service.Book.RecordGhost(testEscrow, 1, "poc_unavailable_host"); err != nil {
		t.Fatalf("RecordGhost(): %v", err)
	}
	if err := service.Flush(); err != nil {
		t.Fatalf("Flush(): %v", err)
	}

	if _, err := service.ResetEpoch(testEpoch); err != nil {
		t.Fatalf("ResetEpoch(): %v", err)
	}

	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	reloaded := NewBook(func() time.Time { return time.Unix(0, 0).UTC() })
	if err := reloaded.Restore(snapshot); err != nil {
		t.Fatalf("Restore(): %v", err)
	}
	if records := reloaded.Query(QueryFilter{}); len(records) != 0 {
		t.Fatalf("got %d records after restarting over a cleared ledger, want none", len(records))
	}
}

// Test flow:
//  1. Record a race with two finished, winning attempts on nonces 10 and 12, ahead of any latest-nonce sweep.
//  2. Assert no record reports a nonzero overcounted total.
func TestNoncesSeenBetweenSweepsDoNotReadAsADisagreement(t *testing.T) {
	book := newTestBook(t, 2)

	if err := book.RecordRace(testEscrow, []Attempt{
		{Nonce: 10, Sent: true, Acknowledged: true, Usage: UsageWinner},
		{Nonce: 12, Sent: true, Acknowledged: true, Usage: UsageWinner},
	}); err != nil {
		t.Fatalf("RecordRace(): %v", err)
	}
	markFinished(t, book, testEscrow, 10, 12)

	for _, record := range book.Query(QueryFilter{}) {
		if record.Overcounted != 0 {
			t.Fatalf("participant %s overcounted %d nonces it was told about before the next sweep",
				record.Participant, record.Overcounted)
		}
	}
}

// Test flow:
//  1. Record a timeout for nonce 6 with no timeout action yet, before any race ever touched it.
//  2. Assert the slot has no disposition and counts the nonce as pending.
//  3. Record the same nonce's timeout again, now with action "completed".
//  4. Assert the disposition becomes `DispositionUnfinishedRefused`, not a ghost.
func TestANonceRecordedOnlyByATimeoutStaysPendingThenReadsRefused(t *testing.T) {
	book := newTestBook(t, 4)
	slot := slotOfNonce(6, 4)

	if err := book.RecordTimeout(testEscrow, 6, "execution", "", "none"); err != nil {
		t.Fatalf("RecordTimeout(): %v", err)
	}
	if dispositions := dispositionsOfSlot(t, book, slot); len(dispositions) != 0 {
		t.Fatalf("dispositions = %v, want none while the never-dispatched nonce awaits a timeout action", dispositions)
	}
	if pending := unclassifiedOfSlot(t, book, slot); pending != 1 {
		t.Fatalf("pending = %d, want the never-dispatched nonce awaiting its timeout action", pending)
	}

	if err := book.RecordTimeout(testEscrow, 6, "execution", "completed", "none"); err != nil {
		t.Fatalf("RecordTimeout(): %v", err)
	}
	assertDisposition(t, book, 6, 4, DispositionUnfinishedRefused)
}

// Test flow:
//  1. Record a ghost on nonce 4, then record its timeout with kind "refused" and action "completed".
//  2. Find the counter carrying that ghost reason.
//  3. Assert its timeout action is "completed" and its disposition stays `DispositionGhost`.
func TestAChargedBurnCarriesItsTimeoutOutcome(t *testing.T) {
	book := newTestBook(t, 2)
	if err := book.RecordGhost(testEscrow, 4, "participant_window_full_no_send"); err != nil {
		t.Fatalf("RecordGhost: %v", err)
	}

	if err := book.RecordTimeout(testEscrow, 4, "refused", "completed", "none"); err != nil {
		t.Fatalf("RecordTimeout: %v", err)
	}

	var charged *CounterRecord
	for _, record := range book.Query(QueryFilter{}) {
		for index, counter := range record.Counters {
			if counter.GhostReason == "participant_window_full_no_send" {
				charged = &record.Counters[index]
			}
		}
	}
	if charged == nil {
		t.Fatal("no counter for the burned nonce")
	}
	if charged.TimeoutAction != "completed" {
		t.Errorf("timeout action = %q, want the vote's own outcome", charged.TimeoutAction)
	}
	if charged.Disposition != DispositionGhost {
		t.Errorf("disposition = %q, want it to stay a ghost", charged.Disposition)
	}
}
