package completionapi

import (
	"encoding/json"
	"log"
)

type ModifiedRequest struct {
	NewBody                  []byte
	OriginalLogprobsValue    *bool
	OriginalTopLogprobsValue *int
}

func ModifyRequestBody(requestBytes []byte, defaultSeed int32) (*ModifiedRequest, error) {
	// Unmarshal the JSON request
	var requestMap map[string]interface{}
	if err := json.Unmarshal(requestBytes, &requestMap); err != nil {
		return nil, err
	}

	originalLogprobsValue := getOriginalLogprobs(requestMap)
	if originalLogprobsValue == nil || *originalLogprobsValue == false {
		requestMap["logprobs"] = true
	}

	originalTopLogprobsValue := getOriginalTopLogprobs(requestMap)
	if originalTopLogprobsValue == nil || *originalTopLogprobsValue < 3 {
		requestMap["top_logprobs"] = 3
	}

	if _, ok := requestMap["seed"]; !ok {
		requestMap["seed"] = defaultSeed
	}

	// Marshal the map back into JSON bytes
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
