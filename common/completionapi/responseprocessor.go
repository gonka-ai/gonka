package completionapi

import (
	"encoding/json"
	"errors"
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
	patcher           idPatcher
	rewriteBuf        []byte
}

func NewExecutorResponseProcessor(inferenceId string) *ExecutorResponseProcessor {
	return &ExecutorResponseProcessor{
		inferenceId: inferenceId,
		patcher:     idPatcher{id: inferenceId},
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
	var err error
	rt.rewriteBuf, err = rt.patcher.rewrite(rt.rewriteBuf[:0], []byte(line))
	updatedLine := string(rt.rewriteBuf)
	if err != nil {
		// Preserve prior semantics: retain the original line on rewrite failure
		// so finish still has something to inspect, then surface the error.
		updatedLine = line
	}
	rt.streamedResponse = append(rt.streamedResponse, updatedLine)
	return updatedLine, err
}

// addOrReplaceIdValue is the non-streamed (whole JSON body) path. Streamed
// chunks use the surgical idPatcher instead — see idrewrite.go.
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
