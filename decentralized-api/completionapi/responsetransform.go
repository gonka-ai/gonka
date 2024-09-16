package completionapi

import "encoding/json"

type ResponseProcessor interface {
	AddIdToJsonResponse(responseBytes []byte) ([]byte, error)

	GetResponseBytes() ([]byte, error)
}

type ExecutorResponseProcessor struct {
	inferenceId   string
	responseBytes []byte
}

func NewExecutorResponseProcessor(inferenceId string) *ExecutorResponseProcessor {
	return &ExecutorResponseProcessor{
		inferenceId:   inferenceId,
		responseBytes: nil,
	}
}

func (rt ExecutorResponseProcessor) AddIdToJsonResponse(responseBytes []byte) ([]byte, error) {
	var bodyMap map[string]interface{}
	err := json.Unmarshal(responseBytes, &bodyMap)
	if err != nil {
		return nil, err
	}

	bodyMap["id"] = rt.inferenceId

	updatedBodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, err
	}

	rt.responseBytes = updatedBodyBytes

	return updatedBodyBytes, nil
}

func (rt ExecutorResponseProcessor) GetResponseBytes() ([]byte, error) {
	return rt.responseBytes, nil
}
