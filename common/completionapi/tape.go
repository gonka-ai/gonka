package completionapi

import "encoding/json"

// TapeArg is the vllm_xargs flag under which the gonka_poc plugin samples a
// request on the public tape of its seed, and scores a replay against it.
const TapeArg = "gonka_tape"

// SetTape flags a request for the tape.
func SetTape(requestMap map[string]interface{}) {
	xargs, _ := requestMap["vllm_xargs"].(map[string]interface{})
	if xargs == nil {
		xargs = map[string]interface{}{}
	}
	xargs[TapeArg] = 1
	requestMap["vllm_xargs"] = xargs
}

// WithTape flags an executor request body for the tape. Tape sampling makes
// processed logprobs one-hot, so the response carries raw ones.
func WithTape(body []byte) ([]byte, error) {
	var requestMap map[string]interface{}
	if err := json.Unmarshal(body, &requestMap); err != nil {
		return nil, err
	}
	SetTape(requestMap)
	requestMap["logprobs_mode"] = "raw_logprobs"
	return json.Marshal(requestMap)
}
