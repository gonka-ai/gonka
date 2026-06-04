package paramvalidators

import (
	"errors"
	"fmt"
)

// ErrStreamOptionsShape covers the wrapper-level rejection: stream_options must be a JSON
// object when present.
var ErrStreamOptionsShape = errors.New("stream_options: invalid wrapper shape")

// streamOptionsWhitelist lists the sub-fields of `stream_options` that survive the
// sanitizer. `include_usage` is the OpenAI-documented field that opts the client into a
// final-chunk `usage` object during streaming — clients need it for token accounting and
// billing. Every other sub-field is dropped:
//
//   - `continuous_usage_stats`: triggers vLLM-project/vllm#9028 (per-chunk usage counter is
//     wrong under chunked prefill). Clients don't know about the upstream bug; the gateway
//     is the right place to neutralize it.
//   - any other / future sub-field: default-deny keeps the surface narrow as upstream vLLM
//     adds new options. Worst-case the client loses access to a niche feature; never a
//     correctness regression.
var streamOptionsWhitelist = map[string]struct{}{
	"include_usage": {},
}

// StreamOptionsValidator enforces a strict sub-field whitelist on the OpenAI
// `stream_options` object. The validator mutates ValidatorContext.Document in place: it
// rejects malformed wrappers, strips the field entirely when `stream` is not exactly
// `true` (the field is meaningless without streaming per the OpenAI spec, and clients
// that send it alongside `stream:false` or omit `stream` are misusing the API), and
// otherwise rewrites the object to contain only whitelisted keys. If the rewrite leaves
// the object empty, the field is dropped from the document so it does not reach the
// upstream as `{}`.
type StreamOptionsValidator struct{}

func (v StreamOptionsValidator) Validate(vctx ValidatorContext) error {
	raw, exists := vctx.Document["stream_options"]
	if !exists {
		return nil
	}
	if stream, _ := vctx.Document["stream"].(bool); !stream {
		delete(vctx.Document, "stream_options")
		return nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: must be an object", ErrStreamOptionsShape)
	}
	for key := range obj {
		if _, allowed := streamOptionsWhitelist[key]; !allowed {
			delete(obj, key)
		}
	}
	if len(obj) == 0 {
		delete(vctx.Document, "stream_options")
	}
	return nil
}
