package accounting

import (
	"testing"

	"devshard/types"
)

// Test flow:
//  1. Open a book and observe one inference that finished, was challenged, and was then validated.
//  2. Sum the nonce totals across the queried records.
//  3. Assert it counts as a finished nonce, not an error: a challenge a validator upheld leaves the host's tokens counted.
func TestAValidatedNonceIsCountedAsFinished(t *testing.T) {
	book := newTestBook(t, 4)
	if err := book.ObserveLatestNonce(testEscrow, 12); err != nil {
		t.Fatalf("ObserveLatestNonce(): %v", err)
	}

	if err := book.ObserveInferences(testEscrow, map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusValidated, InputLength: 4_000, MaxTokens: 256, InputTokens: 900, OutputTokens: 100},
	}); err != nil {
		t.Fatalf("ObserveInferences(): %v", err)
	}

	var totals nonceTotals
	for _, record := range book.Query(QueryFilter{}) {
		totals.add(record.nonceTotals)
	}

	if totals.CountedNonces != 1 || totals.EstimatedError != 0 || totals.InputTokens != 900 {
		t.Fatalf("counted = %d, estimated error = %d, input = %d, want the validated nonce counted with its 900 input tokens", totals.CountedNonces, totals.EstimatedError, totals.InputTokens)
	}
}
