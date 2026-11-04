package completionapi

import "encoding/json"

// TapeArg is the vllm_xargs flag under which the inference fork samples a request
// on the public tape of its seed, and scores a replay against it.
const TapeArg = "gonka_tape"

// SetTape flags a request for the tape. The flag replaces a caller's vllm_xargs,
// which could otherwise name the arguments of a replay. Nothing reads the logprobs
// beyond their token ids, so the response carries the raw ones: a processed mode
// sends the whole batch of the request down the slower sampling path. Beam search
// draws nothing from the tape and a replay of it scores nothing, so it is dropped.
func SetTape(requestMap map[string]interface{}) {
	requestMap["vllm_xargs"] = map[string]interface{}{TapeArg: 1}
	requestMap["logprobs_mode"] = "raw_logprobs"
	delete(requestMap, "use_beam_search")
}

// WithTape flags an executor request body for the tape. A caller's enforced
// tokens would pin the sample, so they are dropped.
func WithTape(body []byte) ([]byte, error) {
	var requestMap map[string]interface{}
	if err := json.Unmarshal(body, &requestMap); err != nil {
		return nil, err
	}
	SetTape(requestMap)
	delete(requestMap, "enforced_tokens")
	delete(requestMap, "enforced_str")
	return json.Marshal(requestMap)
}
