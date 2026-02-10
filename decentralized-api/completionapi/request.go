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
	var requestMap map[string]interface{}
	if err := json.Unmarshal(requestBytes, &requestMap); err != nil {
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
