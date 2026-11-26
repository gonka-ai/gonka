package filters

import (
	"strconv"

	"common/completionapi"
)

const (
	tokenIDBase = 10
	tokenIDBits = 32
)

func dropOutOfVocabularyLogitBiasKeys(document *Document, vocabulary func(model string) int, routedModel string) {
	biases, ok := document.Object(logitBiasField)
	if !ok || len(biases) == 0 {
		return
	}
	tokenIDLimit := completionapi.TokenIDLimit(vocabularySizeFor(vocabulary, routedModel))
	dropped := false
	for key := range biases {
		if tokenID, err := strconv.ParseInt(key, tokenIDBase, tokenIDBits); err == nil && tokenID >= tokenIDLimit {
			delete(biases, key)
			dropped = true
		}
	}
	if dropped {
		document.Set(logitBiasField, biases)
	}
}

func vocabularySizeFor(vocabulary func(model string) int, routedModel string) int {
	if vocabulary == nil {
		return VocabularySizeUnknown
	}
	return vocabulary(routedModel)
}
