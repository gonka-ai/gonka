package completionapi

import (
	"encoding/json"
	"errors"
	"strconv"

	"common/logging"

	"github.com/productscience/inference/x/inference/types"
)

// ErrNoResponseCollected is returned when neither a JSON body nor a chunk arrived.
var ErrNoResponseCollected = errors.New("ExecutorResponseProcessor: can't get response; both jsonResponseBytes and streamedResponse are empty")

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
	forwardLogprobs   bool
	observedUsage     *Usage
	usageRefused      bool
}

func NewExecutorResponseProcessor(inferenceId string, forwardLogprobs bool) *ExecutorResponseProcessor {
	return &ExecutorResponseProcessor{
		inferenceId:       inferenceId,
		jsonResponseBytes: nil,
		streamedResponse:  nil,
		forwardLogprobs:   forwardLogprobs,
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

// prepareBody parses one chunk once and answers both readers: only the forwarded copy can lose logprobs,
// because the validator replays the stored one.
func (rt *ExecutorResponseProcessor) prepareBody(body []byte) (stored, forwarded []byte, err error) {
	document, err := decodeDocumentWithoutUnreadFields(body)
	if err != nil {
		return nil, nil, err
	}
	object, isObject := document.(map[string]any)
	if !isObject {
		return nil, nil, errors.New("ExecutorResponseProcessor: response body is not a JSON object")
	}
	object["id"] = rt.inferenceId
	rt.observeUsage(object)
	dropFields(document, fieldsNoValidatorReads)

	// Only a caller that asked is owed the host's own positions, so only it pays for a copy.
	if rt.forwardLogprobs {
		if forwarded, err = json.Marshal(document); err != nil {
			return nil, nil, err
		}
	}

	// A chunk that will not slim is stored as it arrived rather than failing the inference.
	if err := compressLogprobsIn(document); err != nil {
		logging.Warn("Storing the response whole: it did not compress", types.Inferences,
			"inference_id", rt.inferenceId, "error", err)
	}
	if stored, err = json.Marshal(document); err != nil {
		return nil, nil, err
	}

	if !rt.forwardLogprobs {
		dropFields(document, fieldsOnlyAskingCallersSee)
		if forwarded, err = json.Marshal(document); err != nil {
			return nil, nil, err
		}
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
	return nil, ErrNoResponseCollected
}

func (rt *ExecutorResponseProcessor) GetResponse() (CompletionResponse, error) {
	if rt.jsonResponseBytes != nil {
		return NewCompletionResponseFromBytes(rt.jsonResponseBytes)
	} else if rt.streamedResponse != nil {
		return NewCompletionResponseFromLines(rt.streamedResponse)
	}

	return nil, ErrNoResponseCollected
}

// observeUsage keeps the first non-empty usage, sparing a second parse.
func (rt *ExecutorResponseProcessor) observeUsage(object map[string]any) {
	if rt.observedUsage != nil {
		return
	}
	reported, isObject := object["usage"].(map[string]any)
	if !isObject {
		return
	}
	promptTokens, promptRead := tokenCount(reported["prompt_tokens"])
	completionTokens, completionRead := tokenCount(reported["completion_tokens"])
	if !promptRead || !completionRead {
		// The full re-parse fails on this chunk, so no later usage may stand in for it.
		rt.usageRefused = true
		return
	}
	usage := Usage{PromptTokens: promptTokens, CompletionTokens: completionTokens}
	if usage.IsEmpty() {
		return
	}
	rt.observedUsage = &usage
}

// GetUsage falls back to the full re-parse when no usage chunk could be read.
func (rt *ExecutorResponseProcessor) GetUsage() (*Usage, error) {
	if rt.observedUsage != nil && !rt.usageRefused {
		return rt.observedUsage, nil
	}
	response, err := rt.GetResponse()
	if err != nil {
		return nil, err
	}
	return response.GetUsage()
}

// tokenCount reads a count exactly as the uint64 field of Usage would, and refuses what it cannot.
func tokenCount(reported any) (uint64, bool) {
	if reported == nil {
		return 0, true
	}
	number, isNumber := reported.(json.Number)
	if !isNumber {
		return 0, false
	}
	count, err := strconv.ParseUint(number.String(), 10, 64)
	if err != nil {
		return 0, false
	}
	return count, true
}
