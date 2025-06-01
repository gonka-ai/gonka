package completionapi

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"log"
	"testing"
)

const (
	jsonBody = `{
        "temperature": 0.8,
        "model": "unsloth/llama-3-8b-Instruct",
        "messages": [{
            "role": "system",
            "content": "Regardless of the language of the question, answer in english"
        },
        {
            "role": "user",
            "content": "When did Hawaii become a state?"
        }]
    }`

	jsonBodyNullLogprobs = `{
        "temperature": 0.8,
        "model": "unsloth/llama-3-8b-Instruct",
        "messages": [{
            "role": "system",
            "content": "Regardless of the language of the question, answer in english"
        },
        {
            "role": "user",
            "content": "When did Hawaii become a state?"
        }],
		"logprobs": null
    }`

	jsonBodyStreamNoStreamOptions = `{
        "model": "Qwen/Qwen2.5-7B-Instruct",
        "temperature": 0.8,
        "stream": true,
        "messages": [
          { "role": "user", "content": "Hi!" }
        ]
    }`

	jsonBodyStreamWithStreamOptions = `{
        "model": "Qwen/Qwen2.5-7B-Instruct",
        "temperature": 0.8,
        "stream": true,
		"stream_options": {"include_usage": false},
        "messages": [
          { "role": "user", "content": "Hi!" }
        ]
    }`
)

func Test(t *testing.T) {
	r, err := ModifyRequestBody([]byte(jsonBodyNullLogprobs), 7)
	if err != nil {
		panic(err)
	}
	if r.OriginalLogprobsValue != nil {
		t.Fatalf("expected nil, got %v", r.OriginalLogprobsValue)
	}
	if r.OriginalTopLogprobsValue != nil {
		t.Fatalf("expected nil, got %v", r.OriginalTopLogprobsValue)
	}
	log.Printf(string(r.NewBody))
}

func TestStreamOptions_NoOptions(t *testing.T) {
	r, err := ModifyRequestBody([]byte(jsonBodyStreamNoStreamOptions), 7)
	require.NoError(t, err)
	require.NotNil(t, r)
	var requestMap map[string]interface{}
	if err := json.Unmarshal(r.NewBody, &requestMap); err != nil {
		require.NoError(t, err, "failed to unmarshal request body")
	}

	require.NotNil(t, requestMap["stream_options"])
	require.True(t, requestMap["stream_options"].(map[string]interface{})["include_usage"].(bool), "expected include_usage to be true")
	log.Printf(string(r.NewBody))
}

func TestStreamOptions_WithOptions(t *testing.T) {
	r, err := ModifyRequestBody([]byte(jsonBodyStreamWithStreamOptions), 7)
	require.NoError(t, err)
	require.NotNil(t, r)
	var requestMap map[string]interface{}
	if err := json.Unmarshal(r.NewBody, &requestMap); err != nil {
		require.NoError(t, err, "failed to unmarshal request body")
	}

	require.NotNil(t, requestMap["stream_options"])
	require.True(t, requestMap["stream_options"].(map[string]interface{})["include_usage"].(bool), "expected include_usage to be true")
	log.Printf(string(r.NewBody))
}
