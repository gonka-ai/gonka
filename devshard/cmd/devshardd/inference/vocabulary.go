package inference

import "common/vocabulary"

// VocabularyResolver returns the vocab size of the model the chain pins for an (epoch, model), or 0 when it is unknown.
// The Hugging Face implementation lives in common/vocabulary, shared with the gateway.
type VocabularyResolver = vocabulary.Resolver
