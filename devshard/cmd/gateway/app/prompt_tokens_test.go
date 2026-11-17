package app

import (
	"testing"

	"devshard/cmd/gateway/chain"
)

// Test flow:
//  1. Build the prompt counter without a chain query.
//  2. Read the epoch the counter pins through a snapshot at epoch 7 and through one that has not seen an epoch yet.
//  3. Assert the counter is nil, so no prompt is checked, the first epoch is 7 and known, and the second is unknown, so no pin is read for it.
func TestThePromptCounterNeedsAChainAndAnEpoch(t *testing.T) {
	if counter := promptTokens(nil, fixedPhase{chain.PhaseSnapshot{EpochIndex: 7}}); counter != nil {
		t.Fatalf("prompt counter without a chain = %v, want nil", counter)
	}

	epoch, known := knownEpoch(fixedPhase{chain.PhaseSnapshot{EpochIndex: 7}})()
	_, knownBeforeTheFirstEpoch := knownEpoch(fixedPhase{chain.PhaseSnapshot{}})()

	if epoch != 7 || !known || knownBeforeTheFirstEpoch {
		t.Fatalf("epoch = %d, %v at epoch 7 and known = %v before the first epoch, want 7, true and false", epoch, known, knownBeforeTheFirstEpoch)
	}
}
