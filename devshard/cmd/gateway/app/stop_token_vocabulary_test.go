package app

import (
	"context"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/filters"
)

type recordedVocabularyLookup struct {
	epoch uint64
	model string
}

type fakeVocabularyResolver struct {
	size    int
	lookups []recordedVocabularyLookup
}

func (resolver *fakeVocabularyResolver) Resolve(_ context.Context, epoch uint64, model string) int {
	resolver.lookups = append(resolver.lookups, recordedVocabularyLookup{epoch: epoch, model: model})
	return resolver.size
}

type fixedPhase struct{ snapshot chain.PhaseSnapshot }

func (phase fixedPhase) Snapshot() chain.PhaseSnapshot { return phase.snapshot }

// Test flow:
//  1. Build the stop-token vocabulary over a resolver and a chain snapshot at epoch 7.
//  2. Ask it for a model's size.
//  3. Assert it returns the resolver's answer for exactly that epoch and model, the ones executor and validator resolve.
func TestTheStopTokenVocabularyResolvesTheCurrentEpochsModel(t *testing.T) {
	resolver := &fakeVocabularyResolver{size: 151_936}
	vocabulary := epochStopTokenVocabulary(resolver, fixedPhase{chain.PhaseSnapshot{EpochIndex: 7}})

	size := vocabulary("Qwen/Test")

	if size != 151_936 {
		t.Errorf("vocabulary size = %d, want 151936", size)
	}
	if len(resolver.lookups) != 1 || resolver.lookups[0] != (recordedVocabularyLookup{epoch: 7, model: "Qwen/Test"}) {
		t.Errorf("lookups = %+v, want one for epoch 7 and Qwen/Test", resolver.lookups)
	}
}

// Test flow:
//  1. Build the stop-token vocabulary over a chain snapshot that has not seen an epoch yet.
//  2. Ask it for a model's size.
//  3. Assert it answers unknown without asking the resolver: there is no epoch to pin a model to.
func TestTheStopTokenVocabularyIsUnknownBeforeTheFirstEpoch(t *testing.T) {
	resolver := &fakeVocabularyResolver{size: 151_936}
	vocabulary := epochStopTokenVocabulary(resolver, fixedPhase{chain.PhaseSnapshot{}})

	size := vocabulary("Qwen/Test")

	if size != filters.VocabularySizeUnknown || len(resolver.lookups) != 0 {
		t.Errorf("vocabulary size = %d after %d lookups, want unknown after none", size, len(resolver.lookups))
	}
}
