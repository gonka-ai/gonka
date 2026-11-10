package completionapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// stop_reason was one field of many, so the shapes a serving engine really sends must all decode.
func TestTypedDecodeTakesTheShapesAModelSends(t *testing.T) {
	for name, chunk := range map[string]string{
		"nulls where a value is optional": `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m",` +
			`"system_fingerprint":null,"choices":[{"index":0,"delta":{"content":"Hi"},"logprobs":null,` +
			`"finish_reason":null,"stop_reason":null}]}`,
		"stop_reason as a token id":    `{"id":"c","choices":[{"index":0,"delta":{},"stop_reason":163586}]}`,
		"stop_reason as a stop string": `{"id":"c","choices":[{"index":0,"delta":{},"stop_reason":"<|im_end|>"}]}`,
		"fields the struct never declared": `{"id":"c","choices":[{"index":0,"delta":{"content":"Hi"},"token_ids":[7]}],` +
			`"prompt_text":"hi","metrics":{"ttft_ms":12.5},"routed_experts":"base64=="}`,
		"usage absent":      `{"id":"c","choices":[{"index":0,"delta":{"content":"Hi"}}]}`,
		"usage null":        `{"id":"c","choices":[{"index":0,"delta":{"content":"Hi"}}],"usage":null}`,
		"no choices at all": `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[]}`,
		"completions logprobs shape": `{"id":"c","choices":[{"index":0,"text":"Hi","logprobs":{"tokens":["Hi"],` +
			`"token_logprobs":[-0.5],"top_logprobs":[{"Hi":-0.5}]}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var response Response
			require.NoError(t, json.Unmarshal([]byte(chunk), &response))
		})
	}
}

// Where the strictness still bites: any other field with an unexpected type fails the whole decode,
// which fails the inference. This pins the cost so the next such field is a decision, not a surprise.
func TestTypedDecodeRefusesAWrongTypeInAnotherField(t *testing.T) {
	for name, chunk := range map[string]string{
		"finish_reason as a number": `{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":42}]}`,
		"index as a string":         `{"id":"c","choices":[{"index":"0","delta":{}}]}`,
		"created as a string":       `{"id":"c","created":"1788420678","choices":[]}`,
		"token count as a string":   `{"id":"c","choices":[],"usage":{"prompt_tokens":"7","completion_tokens":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var response Response
			require.Error(t, json.Unmarshal([]byte(chunk), &response))
		})
	}
}
