package completionapi

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"

	"common/logging"
)

// ForcedTopLogprobs is the top_logprobs the validator forces; must equal the devshard gateway's TopLogprobsForcedValue so executor and validator widths are comparable (H1 #3853145).
const ForcedTopLogprobs = 5

type ModifiedRequest struct {
	NewBody []byte
}

const MinTokensFloor = 64

func ModifyRequestBody(requestBytes []byte, defaultSeed int32) (*ModifiedRequest, error) {
	return ModifyRequestBodyWithLogprobsMode(requestBytes, defaultSeed, "")
}

func ModifyRequestBodyWithLogprobsMode(requestBytes []byte, defaultSeed int32, logprobsMode string) (*ModifiedRequest, error) {
	var requestMap map[string]interface{}
	if err := json.Unmarshal(requestBytes, &requestMap); err != nil {
		return nil, err
	}
	if err := validateOpenAICompatRequestMap(requestMap); err != nil {
		return nil, err
	}

	if err := validateMessageContents(requestMap); err != nil {
		return nil, err
	}

	originalLogprobsValue := getOriginalLogprobs(requestMap)
	if originalLogprobsValue == nil || *originalLogprobsValue == false {
		requestMap["logprobs"] = true
	}

	// Pin top_logprobs to the protocol constant on both the original and the validation request; a larger client-supplied value must not pass through.
	requestMap["top_logprobs"] = ForcedTopLogprobs

	EnforceTokenBudgetFloor(requestMap)
	requestMap["skip_special_tokens"] = false
	requestMap["return_token_ids"] = true
	if _, ok := requestMap["seed"]; !ok {
		requestMap["seed"] = defaultSeed
	}

	// Use safe type assertion to avoid panic on malformed input
	if doStream, ok := requestMap["stream"]; ok {
		if doStreamBool, isBool := doStream.(bool); isBool && doStreamBool {
			if streamOpts, exists := requestMap["stream_options"]; !exists {
				requestMap["stream_options"] = map[string]interface{}{"include_usage": true}
			} else if streamOptsMap, isMap := streamOpts.(map[string]interface{}); isMap {
				streamOptsMap["include_usage"] = true
			} else {
				// stream_options exists but is not a map - replace with valid map
				logging.Warn("Malformed stream_options field received, replacing with defaults",
					types.Inferences, "stream_options_value", fmt.Sprintf("%v", streamOpts))
				requestMap["stream_options"] = map[string]interface{}{"include_usage": true}
			}
		}
	}

	if logprobsMode != "" {
		delete(requestMap, "logprobs_mode")
		requestMap["logprobs_mode"] = logprobsMode
	}

	modifiedRequestBytes, err := json.Marshal(requestMap)
	if err != nil {
		return nil, err
	}

	return &ModifiedRequest{
		NewBody: modifiedRequestBytes,
	}, nil
}

func validateMessageContents(requestMap map[string]interface{}) error {
	rawMessages, ok := requestMap["messages"]
	if !ok || rawMessages == nil {
		return nil
	}

	messages, ok := rawMessages.([]interface{})
	if !ok {
		return fmt.Errorf("messages must be an array")
	}

	for i, rawMessage := range messages {
		message, ok := rawMessage.(map[string]interface{})
		if !ok {
			return fmt.Errorf("messages[%d] must be an object", i)
		}

		content, exists := message["content"]
		if !exists {
			continue
		}
		if content == nil {
			continue
		}

		switch typedContent := content.(type) {
		case string:
			continue
		case []interface{}:
			for j, rawPart := range typedContent {
				part, ok := rawPart.(map[string]interface{})
				if !ok {
					return fmt.Errorf("messages[%d].content[%d] must be an object", i, j)
				}

				partType, ok := part["type"].(string)
				if !ok || partType == "" {
					return fmt.Errorf("messages[%d].content[%d].type must be a string", i, j)
				}

				// TODO(vision-costs): We currently validate and pass through non-text parts
				// (e.g. image_url) but downstream prompt token accounting still often uses
				// flattened text-only content. This can underfund/gas-underprice vision
				// requests. Future fix: include non-text token costs in promptTokenCount
				// before transaction construction.
				if partType != "text" {
					continue
				}

				rawText, exists := part["text"]
				if !exists {
					return fmt.Errorf("messages[%d].content[%d].text is required for type %q", i, j, partType)
				}

				text, ok := rawText.(string)
				if !ok {
					return fmt.Errorf("messages[%d].content[%d].text must be a string", i, j)
				}
				if text == "" {
					return fmt.Errorf("messages[%d].content[%d].text must be a non-empty string", i, j)
				}
			}
		default:
			return fmt.Errorf("messages[%d].content must be a string or an array of typed content parts", i)
		}
	}

	return nil
}

func EnforceTokenBudgetFloor(requestMap map[string]interface{}) {
	maxTokens := max(getMaxTokens(requestMap), MinTokensFloor)
	minTokens := min(max(getMinTokens(requestMap), MinTokensFloor), maxTokens)

	requestMap["min_tokens"] = minTokens
	requestMap["max_tokens"] = maxTokens
	requestMap["max_completion_tokens"] = maxTokens

	// Unsupported: min_tokens>0 makes vLLM mask stop-token logits, so an out-of-vocab id CUDA-asserts the node; the floor is always on, so drop stop_token_ids.
	delete(requestMap, "stop_token_ids")
}

func getMinTokens(requestMap map[string]interface{}) int {
	minTokensValue, ok := requestMap["min_tokens"]
	if !ok {
		return 0
	}
	switch value := minTokensValue.(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return 0
	}
}

func getMaxTokens(requestMap map[string]interface{}) int {
	if maxTokensValue, ok := requestMap["max_tokens"]; ok {
		if maxTokensFloat, ok := maxTokensValue.(float64); ok {
			return int(maxTokensFloat)
		}
		if maxTokensInt, ok := maxTokensValue.(int); ok {
			return maxTokensInt
		}
	}
	if maxCompletionTokensValue, ok := requestMap["max_completion_tokens"]; ok {
		if maxCompletionTokensFloat, ok := maxCompletionTokensValue.(float64); ok {
			return int(maxCompletionTokensFloat)
		}
		if maxCompletionTokensInt, ok := maxCompletionTokensValue.(int); ok {
			return maxCompletionTokensInt
		}
	}
	return calculations.DefaultMaxTokens // Default value if not specified
}

// EffectiveMaxTokens returns the output-token limit the ML execution path will
// apply for this request body: explicit max_tokens / max_completion_tokens when
// present, otherwise calculations.DefaultMaxTokens.
func EffectiveMaxTokens(requestBytes []byte) (uint64, error) {
	var requestMap map[string]interface{}
	if err := json.Unmarshal(requestBytes, &requestMap); err != nil {
		return 0, err
	}
	return uint64(getMaxTokens(requestMap)), nil
}

func getOriginalLogprobs(requestMap map[string]interface{}) *bool {
	logprobsValue, ok := requestMap["logprobs"]
	if !ok {
		return nil
	}

	if logprobsValue == nil {
		return nil
	}

	if logprobsValueBool, ok := logprobsValue.(bool); ok {
		return &logprobsValueBool
	}

	// Interpret any non-boolean value as true
	log.Printf("Original request logprobs = %v", logprobsValue)
	trueValue := true
	return &trueValue
}
