package completionapi

import (
	"encoding/json"
	"errors"

	"common/logging"

	"github.com/productscience/inference/x/inference/types"
)

type ResponseProcessor interface {
	ProcessJsonResponse(responseBytes []byte) ([]byte, error)

	ProcessStreamedResponse(line string) (string, error)

	GetResponseBytes() ([]byte, error)
}

type ExecutorResponseProcessor struct {
	inferenceId       string
	jsonResponseBytes []byte
	forwardedJSON     []byte
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
	stored, forwarded, err := rt.prepareBody(responseBytes)
	if err != nil {
		return nil, err
	}
	rt.jsonResponseBytes = stored
	rt.forwardedJSON = forwarded
	return forwarded, nil
}

// GetForwardedJSONBytes is what the gateway was sent, which is not what was stored: a caller that
// relays the body itself must not relay the slimmed copy.
func (rt *ExecutorResponseProcessor) GetForwardedJSONBytes() []byte {
	return rt.forwardedJSON
}

func (rt *ExecutorResponseProcessor) ProcessStreamedResponse(line string) (string, error) {
	body, isData := streamedLineBody(line)
	if !isData {
		rt.streamedResponse = append(rt.streamedResponse, line)
		return line, nil
	}
	stored, forwarded, err := rt.prepareBody([]byte(body))
	if err != nil {
		rt.streamedResponse = append(rt.streamedResponse, line)
		return line, err
	}
	rt.streamedResponse = append(rt.streamedResponse, DataPrefix+string(stored))
	return DataPrefix + string(forwarded), nil
}

// prepareBody parses one chunk once and answers both readers of it. Only the stored copy loses the
// logprob fields, because a client that asked for logprobs cannot recover what never reached the gateway.
func (rt *ExecutorResponseProcessor) prepareBody(body []byte) (stored, forwarded []byte, err error) {
	document, err := decodeJSONDocument(body)
	if err != nil {
		return nil, nil, err
	}
	object, isObject := document.(map[string]any)
	if !isObject {
		return nil, nil, errors.New("ExecutorResponseProcessor: response body is not a JSON object")
	}
	object["id"] = rt.inferenceId
	dropFields(document, fieldsNoValidatorReads)
	if forwarded, err = json.Marshal(document); err != nil {
		return nil, nil, err
	}
	// A chunk that will not slim is stored whole rather than failing the inference. It is worth a
	// line either way: refusing means the host's own logprobs disagree with each other.
	if err := compressLogprobsIn(document); err != nil {
		logging.Warn("Storing the response whole: it did not compress", types.Inferences,
			"inference_id", rt.inferenceId, "error", err)
		return forwarded, forwarded, nil
	}
	if stored, err = json.Marshal(document); err != nil {
		return nil, nil, err
	}
	return stored, forwarded, nil
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
