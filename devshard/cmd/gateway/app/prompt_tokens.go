package app

import (
	"common/vocabulary"

	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/tokenizer"
)

type pinnedPromptTokens struct {
	library *tokenizer.Library
}

func (p pinnedPromptTokens) Prompt(model string, body []byte) (api.PromptText, bool) {
	return api.TokenizerPrompt(p.library.Counter(model), body)
}

func knownEpoch(snapshots phaseSnapshots) func() (uint64, bool) {
	return func() (uint64, bool) {
		epoch := snapshots.Snapshot().EpochIndex
		return epoch, epoch != chain.EpochIndexUnknown
	}
}

func promptTokens(modelSnapshots vocabulary.EpochGroupDataQuery, snapshots phaseSnapshots) api.PromptTokens {
	if modelSnapshots == nil {
		return nil
	}
	return pinnedPromptTokens{library: tokenizer.NewLibrary(vocabulary.ChainModelSource{Query: modelSnapshots}, knownEpoch(snapshots))}
}
