package accounting

import (
	"maps"
	"slices"
	"testing"
)

const secondTestEscrow = "escrow-2"

// Test flow:
//  1. Open a second escrow one epoch ahead of the default and record a ghost on slot 0 of each escrow.
//  2. Group ghost counts by epoch for the slot-0 participant.
//  3. Assert each epoch shows exactly its own one ghost, not the two merged.
func TestEscrowsFromDifferentEpochsAreNotMerged(t *testing.T) {
	book := newTestBook(t, 2)
	openTestEscrow(t, book, secondTestEscrow, testEpoch+1, 2)

	if err := book.RecordGhost(testEscrow, 0, "poc_unavailable_host"); err != nil {
		t.Fatalf("RecordGhost(%s): %v", testEscrow, err)
	}
	if err := book.RecordGhost(secondTestEscrow, 0, "poc_unavailable_host"); err != nil {
		t.Fatalf("RecordGhost(%s): %v", secondTestEscrow, err)
	}

	ghostsByEpoch := make(map[uint64]uint64)
	for _, record := range book.Query(QueryFilter{}) {
		if record.Participant == participantFor(0) {
			ghostsByEpoch[record.EpochIndex] = record.Dispositions[DispositionGhost]
		}
	}

	want := map[uint64]uint64{testEpoch: 1, testEpoch + 1: 1}
	if !maps.Equal(ghostsByEpoch, want) {
		t.Fatalf("ghosts by epoch = %v, want %v", ghostsByEpoch, want)
	}
}

// Test flow:
//  1. Build a `twoEpochBook` fixture and query its records.
//  2. Assert every record's counters slice has capacity equal to its length, with no leftover pre-allocation.
func TestACountersSliceIsSizedForExactlyWhatItHolds(t *testing.T) {
	book := twoEpochBook(t)

	for _, record := range book.Query(QueryFilter{}) {
		if cap(record.Counters) != len(record.Counters) {
			t.Errorf("%s holds %d counters in a slice of %d: the count that sized it drifted",
				record.Participant, len(record.Counters), cap(record.Counters))
		}
	}
}

// Test flow:
//  1. Open a second escrow and record the same three-attempt race, with three distinct terminals, against both escrows.
//  2. Assert each record holds at least two counters, enough for an order to be visible.
//  3. Assert every record's counters are sorted by `compareCounterRecord`.
func TestCountersComeOutOrderedByEscrowThenSlot(t *testing.T) {
	book := newTestBook(t, 2)
	openTestEscrow(t, book, secondTestEscrow, testEpoch, 2)
	for _, escrowID := range []string{secondTestEscrow, testEscrow} {
		if err := book.RecordRace(escrowID, []Attempt{
			{Nonce: 2, Sent: true, Usage: UsageWinner, Terminal: "won"},
			{Nonce: 4, Sent: true, Usage: UsageLoser, Terminal: "lost"},
			{Nonce: 3, Sent: true, Usage: UsageWinner, Terminal: "won"},
		}); err != nil {
			t.Fatalf("RecordRace(%s): %v", escrowID, err)
		}
		markFinished(t, book, escrowID, 2, 3, 4)
	}

	for _, record := range book.Query(QueryFilter{}) {
		if len(record.Counters) < 2 {
			t.Fatalf("%s holds %d counters, want several for an order to be visible",
				record.Participant, len(record.Counters))
		}
		if !slices.IsSortedFunc(record.Counters, compareCounterRecord) {
			t.Errorf("%s counters came out unordered: %+v", record.Participant, record.Counters)
		}
	}
}

func twoEpochBook(t *testing.T) *Book {
	t.Helper()
	book := newTestBook(t, 2)
	openTestEscrow(t, book, secondTestEscrow, testEpoch+1, 2)
	if err := book.RecordGhost(testEscrow, 0, "poc_unavailable_host"); err != nil {
		t.Fatalf("RecordGhost(%s): %v", testEscrow, err)
	}
	if err := book.RecordGhost(secondTestEscrow, 0, "poc_unavailable_host"); err != nil {
		t.Fatalf("RecordGhost(%s): %v", secondTestEscrow, err)
	}
	return book
}

// Test flow:
//  1. Build a `twoEpochBook` fixture and query it filtered to the default epoch.
//  2. Assert every returned record's epoch matches the filter.
//  3. Assert the filter returned at least one record.
func TestQueryNarrowsToOneEpoch(t *testing.T) {
	book := twoEpochBook(t)

	records := book.Query(QueryFilter{EpochIndex: testEpoch})

	for _, record := range records {
		if record.EpochIndex != testEpoch {
			t.Fatalf("epoch %d present under filter for epoch %d", record.EpochIndex, testEpoch)
		}
	}
	if len(records) == 0 {
		t.Fatal("filtering to a populated epoch returned nothing")
	}
}

// Test flow:
//  1. Build a `twoEpochBook` fixture and query it filtered to the slot-1 participant.
//  2. Assert every returned record's participant matches the filter.
//  3. Assert exactly two records come back, one per epoch that participant holds a slot in.
func TestQueryNarrowsToOneParticipant(t *testing.T) {
	book := twoEpochBook(t)

	records := book.Query(QueryFilter{Participant: participantFor(1)})

	for _, record := range records {
		if record.Participant != participantFor(1) {
			t.Fatalf("participant %q present under filter for %q", record.Participant, participantFor(1))
		}
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want one per epoch for a participant holding a slot in both", len(records))
	}
}

// Test flow:
//  1. Build a `twoEpochBook` fixture and query it with a zero-value filter.
//  2. Assert 4 records come back: two participants in each of two epochs.
func TestQueryWithoutFilterIsTheWholeLedger(t *testing.T) {
	book := twoEpochBook(t)

	if got, want := len(book.Query(QueryFilter{})), 4; got != want {
		t.Fatalf("got %d records, want %d: two participants in each of two epochs", got, want)
	}
}

// Test flow:
//  1. Build a `twoEpochBook` fixture and read its epoch summaries.
//  2. Assert there are two summaries, ascending by epoch index.
//  3. Assert the default epoch's summary covers both slot holders and sums its one recorded ghost.
func TestEpochsSummariseEveryParticipantOfTheEpoch(t *testing.T) {
	book := twoEpochBook(t)

	summaries := book.Epochs(QueryFilter{})

	if len(summaries) != 2 {
		t.Fatalf("got %d summaries, want one per epoch", len(summaries))
	}
	if summaries[0].EpochIndex != testEpoch || summaries[1].EpochIndex != testEpoch+1 {
		t.Fatalf("summaries came back as epochs %d, %d, want ascending %d, %d",
			summaries[0].EpochIndex, summaries[1].EpochIndex, testEpoch, testEpoch+1)
	}
	if summaries[0].Participants != 2 {
		t.Fatalf("epoch %d covered %d participants, want both slot holders", testEpoch, summaries[0].Participants)
	}
	if got := summaries[0].Dispositions[DispositionGhost]; got != 1 {
		t.Fatalf("epoch %d summed %d ghosts, want the one it recorded", testEpoch, got)
	}
}

// Test flow:
//  1. Open a second escrow with the same slot-0 participant as the default escrow.
//  2. Query the slot-0 participant's record and read its slot rows.
//  3. Assert there is exactly one slot row per escrow, each naming its own escrow id.
func TestSlotRowsNameTheirEscrow(t *testing.T) {
	book := newTestBook(t, 2)
	openTestEscrow(t, book, secondTestEscrow, testEpoch, 2)

	slots := book.Query(QueryFilter{Participant: participantFor(0)})[0].Slots

	escrows := make(map[string]int)
	for _, slot := range slots {
		escrows[slot.EscrowID]++
	}
	if len(escrows) != 2 || escrows[testEscrow] != 1 || escrows[secondTestEscrow] != 1 {
		t.Fatalf("slot rows by escrow = %v, want one row from each escrow the participant holds", escrows)
	}
}

// Test flow:
//  1. For each table case of a counter key, call `namesNoReason`, covering an unnamed terminal, an unclassified attempt, a burn with no reason, an unreported race, a named burn, and an ordinary answer.
//  2. Assert the result matches the case's expected boolean.
func TestTheUnknownReasonCheckCatchesWhatNothingCouldName(t *testing.T) {
	tests := []struct {
		name string
		key  CounterKey
		want bool
	}{
		{"a terminal the engine has no name for", CounterKey{Terminal: TerminalUnnamed}, true},
		{"an attempt the race never classified", CounterKey{Terminal: TerminalUnclassified}, true},
		{"a burn kind with no reason", CounterKey{Disposition: DispositionGhost}, true},
		{"a race that reported nothing", CounterKey{Terminal: TerminalUnreported}, false},
		{"a burn that named itself", CounterKey{Disposition: DispositionGhost, GhostReason: "poc_unavailable_host"}, false},
		{"an ordinary answer", CounterKey{Disposition: DispositionFinishedUsed, Terminal: "won"}, false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := namesNoReason(testCase.key); got != testCase.want {
				t.Errorf("namesNoReason() = %v, want %v", got, testCase.want)
			}
		})
	}
}
