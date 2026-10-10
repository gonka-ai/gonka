package main

import (
	"context"

	"common/completionapi"
	"common/vocabulary"
)

// StopTokenVocabulary resolves the vocabulary size of a routed model, 0 when unknown.
type StopTokenVocabulary interface {
	VocabularySize(model string) int
}

// stopTokenVocabulary is wired at gateway start (wireStopTokenVocabulary). While it is nil every
// stop_token_ids request is refused.
var stopTokenVocabulary StopTokenVocabulary

// epochStopTokenVocabulary resolves the vocab of the model the chain pins in the current epoch,
// the same source the executor checks against.
type epochStopTokenVocabulary struct {
	resolver vocabulary.Resolver
	epoch    func() uint64
}

func (v epochStopTokenVocabulary) VocabularySize(model string) int {
	epoch := v.epoch()
	if epoch == 0 {
		return 0
	}
	return v.resolver.Resolve(context.Background(), epoch, model)
}

func wireStopTokenVocabulary(query vocabulary.EpochGroupDataQuery, epoch func() uint64) {
	stopTokenVocabulary = epochStopTokenVocabulary{
		resolver: vocabulary.NewResolver(vocabulary.ChainModelSource{Query: query}),
		epoch:    epoch,
	}
}

// stopTokenIDsHandler keeps stop_token_ids only when every id is inside the model's vocabulary:
// with min_tokens > 0 the engine indexes logits by these ids, so an out-of-range id crashes it.
type stopTokenIDsHandler struct{}

func (stopTokenIDsHandler) Apply(ctx *RequestFilterContext, _ VLLMParameter) error {
	vocabularySize := 0
	if stopTokenVocabulary != nil {
		vocabularySize = stopTokenVocabulary.VocabularySize(ctx.RoutedModel)
	}
	var err error
	ctx.Document.RLockedScope(func(raw map[string]any) {
		err = completionapi.ValidateStopTokenIDs(raw, vocabularySize)
	})
	if err != nil {
		return wrapBadChatRequest(err)
	}
	return nil
}
