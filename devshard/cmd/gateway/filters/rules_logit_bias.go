package filters

import "common/completionapi"

func dropOutOfVocabularyLogitBiasKeys(document *Document, vocabulary func(model string) int, routedModel string) {
	biases, ok := document.Object(logitBiasField)
	if !ok || len(biases) == 0 {
		return
	}
	vocabularySize := VocabularySizeUnknown
	if vocabulary != nil {
		vocabularySize = vocabulary(routedModel)
	}
	tokenIDLimit := completionapi.TokenIDLimit(vocabularySize)
	dropped := false
	for key := range biases {
		if completionapi.TokenIDKeyOutOfRange(key, tokenIDLimit) {
			delete(biases, key)
			dropped = true
		}
	}
	if dropped {
		document.Set(logitBiasField, biases)
	}
}
