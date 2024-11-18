package keeper

import "github.com/productscience/inference/x/inference/types"

const PerTokenCost = 1000

func CalculateCost(inference types.Inference) int64 {
	// Very simple for now. Ultimately we will need to calculate this based on the model, and different
	// values for completion and prompt
	if inference.Status == types.InferenceStatus_STARTED {
		return int64(inference.MaxTokens * PerTokenCost)
	}
	return int64(inference.CompletionTokenCount*PerTokenCost + inference.PromptTokenCount*PerTokenCost)
}
