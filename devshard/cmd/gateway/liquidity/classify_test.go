package liquidity

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func classifyInput(open ...types.InferenceRecord) Input {
	return Input{
		Config:  types.SessionConfig{RefusalTimeout: 60, ExecutionTimeout: 1800},
		Balance: 5_000,
		Open:    open,
		Price:   Price{TokenPrice: 10, BytesPerToken: 4, Slot: 1_000, Priced: true},
		Margins: Margins{TimeoutBuffer: 5 * time.Second, SweepGrace: 120 * time.Second},
		Now:     time.Unix(10_000, 0),
	}
}

// Test flow:
//  1. Table-driven: each case classifies one escrow holding one record of a given status and age, against a refusal window of 65 s, a ladder of 450 s and an execution window of 1 925 s.
//  2. Assert the free, returning, late and stuck amounts and the idle flag the case expects.
func TestClassifyPutsEachRecordInItsClass(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		record types.InferenceRecord
		want   Escrow
	}{
		{
			name:   "a pending record under five minutes old returns the surplus of its input",
			record: types.InferenceRecord{Status: types.StatusPending, StartedAt: 9_900, InputLength: 400, ReservedCost: 10_000},
			want:   Escrow{Free: 5_000, Returning: 3_000, Full: true},
		},
		{
			name:   "a pending record past five minutes but inside the retry ladder is late",
			record: types.InferenceRecord{Status: types.StatusPending, StartedAt: 9_600, InputLength: 400, ReservedCost: 10_000},
			want:   Escrow{Free: 5_000, Late: 10_000, Full: true},
		},
		{
			name:   "a pending record past the retry ladder is stuck",
			record: types.InferenceRecord{Status: types.StatusPending, StartedAt: 9_400, InputLength: 400, ReservedCost: 10_000},
			want:   Escrow{Free: 5_000, Stuck: 10_000, Full: true, Idle: true},
		},
		{
			name:   "a started record under five minutes old returns the surplus of its input",
			record: types.InferenceRecord{Status: types.StatusStarted, StartedAt: 9_900, ConfirmedAt: 9_905, InputLength: 400, ReservedCost: 10_000},
			want:   Escrow{Free: 5_000, Returning: 3_000, Full: true},
		},
		{
			name:   "a started record past five minutes and before its deadline is late",
			record: types.InferenceRecord{Status: types.StatusStarted, StartedAt: 9_000, ConfirmedAt: 9_010, InputLength: 400, ReservedCost: 10_000},
			want:   Escrow{Free: 5_000, Late: 10_000, Full: true},
		},
		{
			name:   "a started record past its deadline is stuck",
			record: types.InferenceRecord{Status: types.StatusStarted, StartedAt: 7_000, ConfirmedAt: 7_010, InputLength: 400, ReservedCost: 10_000},
			want:   Escrow{Free: 5_000, Stuck: 10_000, Full: true, Idle: true},
		},
		{
			name:   "an executor stamp later than the refusal window is clamped to it",
			record: types.InferenceRecord{Status: types.StatusStarted, StartedAt: 8_000, ConfirmedAt: 9_500, InputLength: 400, ReservedCost: 10_000},
			want:   Escrow{Free: 5_000, Stuck: 10_000, Full: true, Idle: true},
		},
		{
			name:   "an unstamped started record is anchored on its start",
			record: types.InferenceRecord{Status: types.StatusStarted, StartedAt: 8_000, InputLength: 400, ReservedCost: 10_000},
			want:   Escrow{Free: 5_000, Stuck: 10_000, Full: true, Idle: true},
		},
		{
			name:   "a challenged record is stuck by what it was charged and keeps nothing busy",
			record: types.InferenceRecord{Status: types.StatusChallenged, StartedAt: 9_990, ActualCost: 700, ReservedCost: 10_000},
			want:   Escrow{Free: 5_000, Stuck: 700, Full: true, Idle: true},
		},
		{
			name:   "a finished record is no class at all",
			record: types.InferenceRecord{Status: types.StatusFinished, StartedAt: 9_990, ReservedCost: 10_000},
			want:   Escrow{Free: 5_000, Full: true, Idle: true},
		},
		{
			name:   "an estimate never exceeds the reservation it comes from",
			record: types.InferenceRecord{Status: types.StatusPending, StartedAt: 9_990, InputLength: math.MaxUint64, ReservedCost: 5},
			want:   Escrow{Free: 5_000, Returning: 5, Full: true},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, testCase.want, Classify(classifyInput(testCase.record)))
		})
	}
}

// Test flow:
//  1. Table-driven: each case varies the balance, the price or the nonce cap of an escrow with no open records; the balance is already net of every reservation in flight.
//  2. Assert whether it is full and whether it is starved.
func TestClassifyCallsAnEscrowFullOnlyWhenItPaysASlot(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name        string
		change      func(input *Input)
		wantFull    bool
		wantStarved bool
	}{
		{name: "a slot exactly", change: func(input *Input) { input.Balance = 1_000 }, wantFull: true},
		{name: "one unit short of a slot", change: func(input *Input) { input.Balance = 999 }, wantStarved: true},
		{name: "a nonce-capped escrow that is not full is not starved", change: func(input *Input) { input.Balance, input.NonceCapReached = 10, true }},
		{name: "an unpriced escrow is never full", change: func(input *Input) { input.Price.Priced = false }, wantStarved: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			input := classifyInput()
			testCase.change(&input)
			got := Classify(input)
			if got.Full != testCase.wantFull || got.Starved != testCase.wantStarved {
				t.Fatalf("Classify() full, starved = %v, %v, want %v, %v", got.Full, got.Starved, testCase.wantFull, testCase.wantStarved)
			}
		})
	}
}

// Test flow:
//  1. Classify an escrow with zero bytes per token holding a fresh pending record.
//  2. Assert it returns nothing for the record but still counts it as keeping the escrow busy.
func TestClassifyEstimatesNoSurplusWithoutABytesPerTokenRatio(t *testing.T) {
	t.Parallel()
	input := classifyInput(types.InferenceRecord{Status: types.StatusPending, StartedAt: 9_990, InputLength: 400, ReservedCost: 10_000})
	input.Price.BytesPerToken = 0

	got := Classify(input)

	if got.Returning != 0 || got.Idle {
		t.Fatalf("Classify() returning, idle = %d, %v, want 0, false", got.Returning, got.Idle)
	}
}
