package completionapi

import (
	"encoding/json"
	"fmt"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"

	"common/logging"
)

// ForcedTopLogprobs is the width devshard pins on every executed request, so executor and validator logprobs stay comparable (H1 #3853145).
const ForcedTopLogprobs = 5

type ModifiedRequest struct {
	NewBody         []byte
	AsksForLogprobs bool
}

const MinTokensFloor = 64

func ModifyRequestBody(requestBytes []byte, defaultSeed int32) (*ModifiedRequest, error) {
	return ModifyRequestBodyWithLogprobsMode(requestBytes, defaultSeed, "")
}

func ModifyRequestBodyWithLogprobsMode(requestBytes []byte, defaultSeed int32, logprobsMode string) (*ModifiedRequest, error) {
	var requestMap map[string]any
	if err := json.Unmarshal(requestBytes, &requestMap); err != nil {
		return nil, err
	}
	if err := validateOpenAICompatRequestMap(requestMap); err != nil {
		return nil, err
	}

	if err := validateMessageContents(requestMap); err != nil {
		return nil, err
	}

	asksForLogprobs := logprobsAsked(requestMap)
	// Pin both fields: anything the engine reads as logprobs-off leaves the inference unvalidatable.
	requestMap["logprobs"] = true
	requestMap["top_logprobs"] = ForcedTopLogprobs

	EnforceTokenBudgetFloor(requestMap)

	// Only clamp when the caller asked: injecting n into a request that never
	// carried it would change the body we sign for a broker that never set it.
	if _, asked := requestMap["n"]; asked {
		requestMap["n"] = 1
	}
	requestMap["skip_special_tokens"] = false
	requestMap["return_token_ids"] = true
	if _, ok := requestMap["seed"]; !ok {
		requestMap["seed"] = defaultSeed
	}

	// Use safe type assertion to avoid panic on malformed input
	if doStream, ok := requestMap["stream"]; ok {
		if doStreamBool, isBool := doStream.(bool); isBool && doStreamBool {
			if streamOpts, exists := requestMap["stream_options"]; !exists {
				requestMap["stream_options"] = map[string]any{"include_usage": true}
			} else if streamOptsMap, isMap := streamOpts.(map[string]any); isMap {
				streamOptsMap["include_usage"] = true
			} else {
				// stream_options exists but is not a map - replace with valid map
				logging.Warn("Malformed stream_options field received, replacing with defaults",
					types.Inferences, "stream_options_value", fmt.Sprintf("%v", streamOpts))
				requestMap["stream_options"] = map[string]any{"include_usage": true}
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
		NewBody:         modifiedRequestBytes,
		AsksForLogprobs: asksForLogprobs,
	}, nil
}

func validateMessageContents(requestMap map[string]any) error {
	rawMessages, ok := requestMap["messages"]
	if !ok || rawMessages == nil {
		return nil
	}

	messages, ok := rawMessages.([]any)
	if !ok {
		return fmt.Errorf("messages must be an array")
	}

	for i, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
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
		case []any:
			for j, rawPart := range typedContent {
				part, ok := rawPart.(map[string]any)
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

func EnforceTokenBudgetFloor(requestMap map[string]any) {
	maxTokens := max(getMaxTokens(requestMap), MinTokensFloor)
	minTokens := min(max(getMinTokens(requestMap), MinTokensFloor), maxTokens)

	requestMap["min_tokens"] = minTokens
	requestMap["max_tokens"] = maxTokens
	requestMap["max_completion_tokens"] = maxTokens

	// Unsupported: min_tokens>0 makes vLLM mask stop-token logits, so an out-of-vocab id CUDA-asserts the node; the floor is always on, so drop stop_token_ids.
	delete(requestMap, "stop_token_ids")
}

func getMinTokens(requestMap map[string]any) int {
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

func getMaxTokens(requestMap map[string]any) int {
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
	var requestMap map[string]any
	if err := json.Unmarshal(requestBytes, &requestMap); err != nil {
		return 0, err
	}
	return uint64(getMaxTokens(requestMap)), nil
}

// MinTokensOf returns the min_tokens the request carries (0 if unset).
func MinTokensOf(requestMap map[string]any) int {
	return getMinTokens(requestMap)
}

// LogprobsAsked reads the pair as one intent: logprobs alone names no width, and a width of zero switches them off.
func LogprobsAsked(logprobs bool, topLogprobs float64) bool {
	return logprobs && topLogprobs > 0
}

// Only an explicit boolean asks; the forcing above then overwrites whatever the caller wrote.
func logprobsAsked(requestMap map[string]any) bool {
	asked, isBool := requestMap["logprobs"].(bool)
	width, isNumber := requestMap["top_logprobs"].(float64)
	return isBool && isNumber && LogprobsAsked(asked, width)
}
