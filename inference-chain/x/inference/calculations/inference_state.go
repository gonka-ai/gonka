package calculations

import "github.com/productscience/inference/x/inference/types"

type InferenceMessage interface{}

type StartInferenceMessage struct {
}

const DefaultMaxTokens = 5000

func ProcessStartInference(currentInference *types.Inference, startMessage *types.MsgStartInference) (*types.Inference, error) {
	if currentInference == nil {
		currentInference = &types.Inference{
			Index:               startMessage.InferenceId,
			InferenceId:         startMessage.InferenceId,
			PromptHash:          startMessage.PromptHash,
			PromptPayload:       startMessage.PromptPayload,
			RequestedBy:         startMessage.RequestedBy,
			Status:              types.InferenceStatus_STARTED,
			Model:               startMessage.Model,
			StartBlockHeight:    0, // This will be set later
			StartBlockTimestamp: 0, // This will be set later
			MaxTokens:           DefaultMaxTokens,
			AssignedTo:          startMessage.AssignedTo,
			NodeVersion:         startMessage.NodeVersion,
		}
	} else {

		currentInference.PromptHash = startMessage.PromptHash
		currentInference.PromptPayload = startMessage.PromptPayload
		currentInference.RequestedBy = startMessage.RequestedBy
		currentInference.Model = startMessage.Model
		currentInference.StartBlockHeight = 0    // This will be set later
		currentInference.StartBlockTimestamp = 0 // This will be set later
		currentInference.MaxTokens = DefaultMaxTokens
		currentInference.AssignedTo = startMessage.AssignedTo
		currentInference.NodeVersion = startMessage.NodeVersion
	}
	if currentInference.EscrowAmount == 0 {
	}

	return currentInference, nil
}
