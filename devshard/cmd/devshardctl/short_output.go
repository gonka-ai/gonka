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

// stopTokenVocabulary is set by wireStopTokenVocabulary; while nil, stop_token_ids are refused.
var stopTokenVocabulary StopTokenVocabulary

// epochStopTokenVocabulary resolves the vocab the chain pins for the current epoch, as the executor does.
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

// stopTokenIDsHandler keeps stop_token_ids only when every id is inside the model's vocabulary.
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
