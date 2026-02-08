package completionapi

import (
	"encoding/json"
	"log"

	"github.com/productscience/inference/x/inference/calculations"
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

	// Ensure reproducible sampling for inference validation:
	// The seed controls the RNG state for token sampling, ensuring that both
	// executor and validator produce identical token sequences given the same
	// seed and model. This is critical for the sequence check in validation.
	//
	// Protection against speculative decoding attacks:
	// Speculative decoding can produce non-deterministic results due to:
	// - Draft model differences across nodes
	// - Floating-point precision variations
	// - Batching and scheduling differences
	//
	// By always setting a seed, we ensure that:
	// 1. All sampling is deterministic based on the seed
	// 2. Validators can reproduce the exact token sequence
	// 3. Any deviation indicates fraud (wrong model, speculative decoding, etc.)
	//
	// Note: When used with inference validation, the run_seed is derived from
	// this seed and the inference_id in the validation layer.
	if _, ok := requestMap["seed"]; !ok {
		requestMap["seed"] = defaultSeed
	}

	if doStream, ok := requestMap["stream"]; ok && doStream.(bool) {
		if _, ok := requestMap["stream_options"]; !ok {
			requestMap["stream_options"] = map[string]interface{}{"include_usage": true}
		} else {
			requestMap["stream_options"].(map[string]interface{})["include_usage"] = true
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
