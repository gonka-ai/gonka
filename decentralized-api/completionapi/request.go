package completionapi

import (
	"encoding/json"
	"log"
)

type ModifiedRequest struct {
	NewBody            []byte
	LogprobsWereSet    bool
	TopLogrpobsWereSet bool
}

func ModifyRequestBody(requestBytes []byte) (*ModifiedRequest, error) {
	// Unmarshal the JSON request
	var requestMap map[string]interface{}
	if err := json.Unmarshal(requestBytes, &requestMap); err != nil {
		return nil, err
	}

	// Check if the map contains keys "logprobs" and "top_logprobs"
	if logprobsValue, ok := requestMap["logprobs"]; ok {
		if _, ok := logprobsValue.(int); !ok {

		}
		log.Println("logprobs found")
	} else {
		requestMap["logprobs"] = true
	}

	if topLogprobsValue, ok := requestMap["top_logprobs"]; ok {
		if _, ok := topLogprobsValue.(int); !ok {

		}
		log.Println("top_logprobs found")
	} else {
		requestMap["top_logprobs"] = 3
	}

	// Marshal the map back into JSON bytes
	modifiedRequestBytes, err := json.Marshal(requestMap)
	if err != nil {
		return nil, err
	}

	return &ModifiedRequest{
		NewBody:            modifiedRequestBytes,
		LogprobsWereSet:    false,
		TopLogrpobsWereSet: false,
	}, nil
}
