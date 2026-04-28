package completionapi

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"

	"decentralized-api/logging"
)

type ModifiedRequest struct {
	NewBody                  []byte
	OriginalLogprobsValue    *bool
	OriginalTopLogprobsValue *int
}

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

	originalTopLogprobsValue := getOriginalTopLogprobs(requestMap)
	if originalTopLogprobsValue == nil || *originalTopLogprobsValue < 5 {
		requestMap["top_logprobs"] = 5
	}

	maxTokens := getMaxTokens(requestMap)

	requestMap["max_tokens"] = maxTokens
	requestMap["max_completion_tokens"] = maxTokens
	requestMap["skip_special_tokens"] = false
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
		NewBody:                  modifiedRequestBytes,
		OriginalLogprobsValue:    originalLogprobsValue,
		OriginalTopLogprobsValue: originalTopLogprobsValue,
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

func getOriginalTopLogprobs(requestMap map[string]interface{}) *int {
	topLogprobsValue, ok := requestMap["top_logprobs"]
	if !ok {
		return nil
	}

	if topLogprobsValue == nil {
		return nil
	}

	if topLogprobsValueInt, ok := topLogprobsValue.(int); ok {
		return &topLogprobsValueInt
	}

	if topLogprobsValueBool, ok := topLogprobsValue.(bool); ok {
		if topLogprobsValueBool {
			one := 1
			return &one
		} else {
			zero := 0
			return &zero
		}
	}

	// Discard any non-integer value
	log.Printf("Original request top_logprobs = %v", topLogprobsValue)
	return nil
}
