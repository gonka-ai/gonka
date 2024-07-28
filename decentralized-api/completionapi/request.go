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

func ModifyRequestBody(requestBytes []byte) (*ModifiedRequest, error) {
	// Unmarshal the JSON request
	var requestMap map[string]interface{}
	if err := json.Unmarshal(requestBytes, &requestMap); err != nil {
		return nil, err
	}

	var originalLogprobsValue *bool
	// Check if the map contains keys "logprobs" and "top_logprobs"
	if logprobsValue, ok := requestMap["logprobs"]; ok {
		if logprobsValueBool, ok := logprobsValue.(bool); !ok {

		} else {
			originalLogprobsValue = &logprobsValueBool
		}
		log.Printf("Original request logprobs = %v", logprobsValue)
	} else {
		originalLogprobsValue = nil
		requestMap["logprobs"] = true
	}

	var originalTopLogprobsValue *int
	if topLogprobsValue, ok := requestMap["top_logprobs"]; ok {
		if topLogprobsValueInt, ok := topLogprobsValue.(int); !ok {

		} else {
			originalTopLogprobsValue = &topLogprobsValueInt
		}
		log.Printf("Original request top_logprobs = %v", topLogprobsValue)
	} else {
		requestMap["top_logprobs"] = 3
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
