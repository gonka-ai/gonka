package app

import (
	"context"

	"common/vocabulary"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/filters"
)

type phaseSnapshots interface {
	Snapshot() chain.PhaseSnapshot
}

// epochStopTokenVocabulary resolves the vocabulary the chain pins for the current epoch, as the executor and the validator do. See ../docs/operations.md, "Stop token ids".
func epochStopTokenVocabulary(resolver vocabulary.Resolver, snapshots phaseSnapshots) func(model string) int {
	return func(model string) int {
		epoch := snapshots.Snapshot().EpochIndex
		if epoch == chain.EpochIndexUnknown {
			return filters.VocabularySizeUnknown
		}
		return resolver.Resolve(context.Background(), epoch, model)
	}
}

// stopTokenVocabulary is nil without a chain, so the filters refuse stop_token_ids rather than send an unproven id.
func stopTokenVocabulary(modelSnapshots vocabulary.EpochGroupDataQuery, snapshots phaseSnapshots) func(model string) int {
	if modelSnapshots == nil {
		return nil
	}
	return epochStopTokenVocabulary(vocabulary.NewResolver(vocabulary.ChainModelSource{Query: modelSnapshots}), snapshots)
}
