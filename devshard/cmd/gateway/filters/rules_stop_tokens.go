package filters

import "common/completionapi"

// validateStopTokenIDs keeps stop_token_ids only when every id is inside the routed model's vocabulary. See README.md, "Stop token ids".
func validateStopTokenIDs(document *Document, vocabulary func(model string) int, routedModel string) error {
	if !completionapi.HasStopTokenIDs(document.Raw()) {
		return nil
	}
	vocabularySize := VocabularySizeUnknown
	if vocabulary != nil {
		vocabularySize = vocabulary(routedModel)
	}
	return WrapReject(completionapi.ValidateStopTokenIDs(document.Raw(), vocabularySize))
}
