package completionapi

import (
	"strings"
	"testing"
)

func TestProcessingJsonResponse(t *testing.T) {
	processor := NewExecutorResponseProcessor("dummy-id")
	processor.ProcessJsonResponse([]byte("dummy-response"))
}

const EVENT = `
data: {"id":"cmpl-3973dab1430143849df83d943ea0c7ac","object":"chat.completion.chunk","created":1726472629,"model":"unsloth/llama-3-8b-Instruct","choices":[{"index":0,"delta":{"content":"9"},"logprobs":{"content":[{"token":"9","logprob":0.0,"bytes":[57],"top_logprobs":[{"token":"9","logprob":0.0,"bytes":[57]},{"token":"8","logprob":-23.125,"bytes":[56]},{"token":"0","logprob":-24.125,"bytes":[48]}]}]},"finish_reason":null}]}
`

func TestProcessingStreamedEvents(t *testing.T) {
	dummyId := "dummy-inference-id"
	processor := NewExecutorResponseProcessor(dummyId)
	var updatedLine string
	var err error
	updatedLine, err = processor.ProcessStreamedResponse(strings.TrimSpace(EVENT))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	println(updatedLine)

	if !strings.Contains(updatedLine, dummyId) {
		t.Fatalf("expected %s to contain %s", updatedLine, dummyId)
	}

	bytes, err := processor.GetResponseBytes()
	if err != nil {
		t.Fatalf("unexpected error for GetResponseBytes: %v", err)
	}

	println(string(bytes))
}
