package app

import (
	"testing"

	"devshard/cmd/gateway/chain"
)

// Test flow:
//  1. Advance a phaseNarrator through a steady first snapshot.
//  2. For each case (the same snapshot again, a new epoch, or requests becoming blocked), advance it with the case's next snapshot.
//  3. Assert the first advance was reported as the first.
//  4. Assert the resulting phaseChange matches the case's expectation.
func TestThePhaseNarratorSpeaksOnlyOnChange(t *testing.T) {
	steady := chain.PhaseSnapshot{EpochIndex: 7, BlockHeight: 100}

	testCases := []struct {
		name string
		next chain.PhaseSnapshot
		want phaseChange
	}{
		{
			name: "the same snapshot again says nothing",
			next: chain.PhaseSnapshot{EpochIndex: 7, BlockHeight: 140},
			want: phaseChange{},
		},
		{
			name: "a new epoch is worth a line",
			next: chain.PhaseSnapshot{EpochIndex: 8, BlockHeight: 140},
			want: phaseChange{epoch: true},
		},
		{
			name: "requests becoming blocked is worth a line",
			next: chain.PhaseSnapshot{EpochIndex: 7, BlockHeight: 140, RequestsBlocked: true, BlockReason: chain.BlockReasonPoC},
			want: phaseChange{block: true},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			narrator := &phaseNarrator{}
			if first := narrator.advance(steady); !first.first {
				t.Fatal("the first snapshot was not reported as the first")
			}

			if got := narrator.advance(testCase.next); got != testCase.want {
				t.Fatalf("advance() = %+v, want %+v", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Advance a phaseNarrator twice through a snapshot with requests blocked.
//  2. Advance it again with a snapshot where the block is gone.
//  3. Assert the resulting phaseChange reports the block cleared.
func TestThePhaseNarratorReportsABlockClearing(t *testing.T) {
	narrator := &phaseNarrator{}
	blocked := chain.PhaseSnapshot{EpochIndex: 7, RequestsBlocked: true, BlockReason: chain.BlockReasonPoC}
	narrator.advance(blocked)
	narrator.advance(blocked)

	cleared := narrator.advance(chain.PhaseSnapshot{EpochIndex: 7})

	if !cleared.block {
		t.Fatal("requests came back and the narrator said nothing, so the block looks permanent in the log")
	}
}

// Test flow:
//  1. Call journalSettings with a nil ledger.
//  2. Assert the resulting settings' Ledger field is a nil interface.
func TestADisabledLedgerIsKeptOutOfTheJournal(t *testing.T) {
	if settings := journalSettings(nil); settings.Ledger != nil {
		t.Fatalf("Ledger = %#v, want a nil interface", settings.Ledger)
	}
}
