package completionapi

import (
	"encoding/json"
	"errors"
	"strings"
)

type ResponseProcessor interface {
	ProcessJsonResponse(responseBytes []byte) ([]byte, error)

	ProcessStreamedResponse(line string) (string, error)

	GetResponseBytes() ([]byte, error)
}

type ExecutorResponseProcessor struct {
	inferenceId       string
	jsonResponseBytes []byte
	streamedResponse  []string
}

func NewExecutorResponseProcessor(inferenceId string) *ExecutorResponseProcessor {
	return &ExecutorResponseProcessor{
		inferenceId:       inferenceId,
		jsonResponseBytes: nil,
		streamedResponse:  nil,
	}
}

func (rt *ExecutorResponseProcessor) ProcessJsonResponse(responseBytes []byte) ([]byte, error) {
	updatedBodyBytes, err := addOrReplaceIdValue(responseBytes, rt.inferenceId)
	if err != nil {
		return nil, err
	}

	rt.jsonResponseBytes = updatedBodyBytes

	return updatedBodyBytes, nil
}

func (rt *ExecutorResponseProcessor) ProcessStreamedResponse(line string) (string, error) {
	updatedLine, err := getUpdatedLine(line, rt.inferenceId)
	rt.streamedResponse = append(rt.streamedResponse, updatedLine)
	return updatedLine, err
}

func getUpdatedLine(line string, id string) (string, error) {
	if !strings.HasPrefix(line, DataPrefix) {
		return line, nil
	}

	trimmed := strings.TrimSpace(strings.TrimPrefix(line, DataPrefix))
	if strings.HasPrefix(trimmed, "[DONE]") {
		return line, nil
	}

	updatedBodyBytes, err := addOrReplaceIdValue([]byte(trimmed), id)
	if err != nil {
		return line, err
	}

	return DataPrefix + string(updatedBodyBytes), nil
}

func addOrReplaceIdValue(bytes []byte, id string) ([]byte, error) {
	var bodyMap map[string]interface{}
	err := json.Unmarshal(bytes, &bodyMap)
	if err != nil {
		return nil, err
	}

	bodyMap["id"] = id

	return json.Marshal(bodyMap)
}

func (rt *ExecutorResponseProcessor) GetResponseBytes() ([]byte, error) {
	if rt.jsonResponseBytes != nil {
		return rt.jsonResponseBytes, nil
	} else if rt.streamedResponse != nil {
		response := SerializedStreamedResponse{
			Events: rt.streamedResponse,
		}
		return json.Marshal(response)
	}
	return rt.jsonResponseBytes, nil
}

func (rt *ExecutorResponseProcessor) GetResponse() (CompletionResponse, error) {
	if rt.jsonResponseBytes != nil {
		return NewCompletionResponseFromBytes(rt.jsonResponseBytes)
	} else if rt.streamedResponse != nil {
		return NewCompletionResponseFromLines(rt.streamedResponse)
	}

	return nil, errors.New("ExecutorResponseProcessor: can't get response; both jsonResponseBytes and streamedResponse are empty")
}
