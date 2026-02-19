package completionapi

import (
	"encoding/json"
	"testing"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/stretchr/testify/require"
)

const (
	jsonBody = `{
        "temperature": 0.8,
        "model": "Qwen/Qwen2.5-7B-Instruct",
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
        "model": "Qwen/Qwen2.5-7B-Instruct",
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

	jsonBodyWithMaxTokens = `{
        "model": "Qwen/Qwen2.5-7B-Instruct",
        "temperature": 0.8,
        "max_tokens": 100,
        "messages": [
          { "role": "user", "content": "Hi!" }
        ]
    }`

	jsonBodyWithMaxCompletionTokens = `{
        "model": "Qwen/Qwen2.5-7B-Instruct",
        "temperature": 0.8,
        "max_completion_tokens": 200,
        "messages": [
          { "role": "user", "content": "Hi!" }
        ]
    }`

	jsonBodyNoTokenLimits = `{
        "model": "Qwen/Qwen2.5-7B-Instruct",
        "temperature": 0.8,
        "messages": [
          { "role": "user", "content": "Hi!" }
        ]
    }`
)

func TestModifyRequestBody_NullLogprobsPreserved(t *testing.T) {
	r, err := ModifyRequestBody([]byte(jsonBodyNullLogprobs), 7)
	require.NoError(t, err)
	require.Nil(t, r.OriginalLogprobsValue)
	require.Nil(t, r.OriginalTopLogprobsValue)
}

func TestStreamOptions_NoOptions(t *testing.T) {
	r, err := ModifyRequestBody([]byte(jsonBodyStreamNoStreamOptions), 7)
	require.NoError(t, err)
	require.NotNil(t, r)
	var requestMap map[string]interface{}
	require.NoError(t, json.Unmarshal(r.NewBody, &requestMap), "failed to unmarshal request body")

	require.NotNil(t, requestMap["stream_options"])
	require.True(t, requestMap["stream_options"].(map[string]interface{})["include_usage"].(bool), "expected include_usage to be true")
}

func TestStreamOptions_WithOptions(t *testing.T) {
	r, err := ModifyRequestBody([]byte(jsonBodyStreamWithStreamOptions), 7)
	require.NoError(t, err)
	require.NotNil(t, r)
	var requestMap map[string]interface{}
	require.NoError(t, json.Unmarshal(r.NewBody, &requestMap), "failed to unmarshal request body")

	require.NotNil(t, requestMap["stream_options"])
	require.True(t, requestMap["stream_options"].(map[string]interface{})["include_usage"].(bool), "expected include_usage to be true")
}

// TestStreamOptions_MalformedStreamValue tests that malformed "stream" field doesn't cause panic
func TestStreamOptions_MalformedStreamValue(t *testing.T) {
	// Test case 1: stream is a string instead of bool
	jsonBodyStreamString := `{
        "model": "test",
        "stream": "true",
        "messages": [{ "role": "user", "content": "Hi!" }]
    }`
	r, err := ModifyRequestBody([]byte(jsonBodyStreamString), 7)
	require.NoError(t, err, "Should not panic or error on string stream value")
	require.NotNil(t, r)
	var requestMap map[string]interface{}
	require.NoError(t, json.Unmarshal(r.NewBody, &requestMap))
	_, exists := requestMap["stream_options"]
	require.False(t, exists, "stream_options should not be added when stream is not a boolean true")

	// Test case 2: stream is a number
	jsonBodyStreamNumber := `{
        "model": "test",
        "stream": 1,
        "messages": [{ "role": "user", "content": "Hi!" }]
    }`
	r, err = ModifyRequestBody([]byte(jsonBodyStreamNumber), 7)
	require.NoError(t, err, "Should not panic or error on number stream value")
	require.NotNil(t, r)
	requestMap = map[string]interface{}{}
	require.NoError(t, json.Unmarshal(r.NewBody, &requestMap))
	_, exists = requestMap["stream_options"]
	require.False(t, exists, "stream_options should not be added when stream is not a boolean true")

	// Test case 3: stream is null
	jsonBodyStreamNull := `{
        "model": "test",
        "stream": null,
        "messages": [{ "role": "user", "content": "Hi!" }]
    }`
	r, err = ModifyRequestBody([]byte(jsonBodyStreamNull), 7)
	require.NoError(t, err, "Should not panic or error on null stream value")
	require.NotNil(t, r)
	requestMap = map[string]interface{}{}
	require.NoError(t, json.Unmarshal(r.NewBody, &requestMap))
	_, exists = requestMap["stream_options"]
	require.False(t, exists, "stream_options should not be added when stream is not a boolean true")
}

// TestStreamOptions_MalformedStreamOptions tests that malformed "stream_options" field doesn't cause panic
func TestStreamOptions_MalformedStreamOptions(t *testing.T) {
	// Test case 1: stream_options is a string instead of object
	jsonBodyStreamOptionsString := `{
        "model": "test",
        "stream": true,
        "stream_options": "invalid",
        "messages": [{ "role": "user", "content": "Hi!" }]
    }`
	r, err := ModifyRequestBody([]byte(jsonBodyStreamOptionsString), 7)
	require.NoError(t, err, "Should not panic or error on string stream_options")
	require.NotNil(t, r)

	// Verify that stream_options was replaced with a valid map
	var requestMap map[string]interface{}
	err = json.Unmarshal(r.NewBody, &requestMap)
	require.NoError(t, err)
	streamOpts, ok := requestMap["stream_options"].(map[string]interface{})
	require.True(t, ok, "stream_options should be a map after processing")
	require.True(t, streamOpts["include_usage"].(bool), "include_usage should be true")

	// Test case 2: stream_options is an array
	jsonBodyStreamOptionsArray := `{
        "model": "test",
        "stream": true,
        "stream_options": [1, 2, 3],
        "messages": [{ "role": "user", "content": "Hi!" }]
    }`
	r, err = ModifyRequestBody([]byte(jsonBodyStreamOptionsArray), 7)
	require.NoError(t, err, "Should not panic or error on array stream_options")
	require.NotNil(t, r)
	requestMap = map[string]interface{}{}
	require.NoError(t, json.Unmarshal(r.NewBody, &requestMap))
	streamOpts, ok = requestMap["stream_options"].(map[string]interface{})
	require.True(t, ok, "stream_options should be a map after processing")
	require.True(t, streamOpts["include_usage"].(bool), "include_usage should be true")

	// Test case 3: stream_options is a number
	jsonBodyStreamOptionsNumber := `{
        "model": "test",
        "stream": true,
        "stream_options": 123,
        "messages": [{ "role": "user", "content": "Hi!" }]
    }`
	r, err = ModifyRequestBody([]byte(jsonBodyStreamOptionsNumber), 7)
	require.NoError(t, err, "Should not panic or error on number stream_options")
	require.NotNil(t, r)
	requestMap = map[string]interface{}{}
	require.NoError(t, json.Unmarshal(r.NewBody, &requestMap))
	streamOpts, ok = requestMap["stream_options"].(map[string]interface{})
	require.True(t, ok, "stream_options should be a map after processing")
	require.True(t, streamOpts["include_usage"].(bool), "include_usage should be true")
}

// TestStreamFalse tests that stream=false doesn't modify stream_options
func TestStreamFalse(t *testing.T) {
	jsonBodyStreamFalse := `{
        "model": "test",
        "stream": false,
        "messages": [{ "role": "user", "content": "Hi!" }]
    }`
	r, err := ModifyRequestBody([]byte(jsonBodyStreamFalse), 7)
	require.NoError(t, err)
	require.NotNil(t, r)

	var requestMap map[string]interface{}
	err = json.Unmarshal(r.NewBody, &requestMap)
	require.NoError(t, err)

	// stream_options should not exist since stream is false
	_, exists := requestMap["stream_options"]
	require.False(t, exists, "stream_options should not be added when stream is false")
}

func TestMaxTokens(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"WithMaxTokens", jsonBodyWithMaxTokens, 100},
		{"WithMaxCompletionTokens", jsonBodyWithMaxCompletionTokens, 200},
		{"NoTokenLimits", jsonBodyNoTokenLimits, calculations.DefaultMaxTokens},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := ModifyRequestBody([]byte(tt.input), 7)
			require.NoError(t, err)
			require.NotNil(t, r)

			var requestMap map[string]interface{}
			err = json.Unmarshal(r.NewBody, &requestMap)
			require.NoError(t, err, "failed to unmarshal request body")

			maxTokens := requestMap["max_tokens"].(float64)
			maxCompletionTokens := requestMap["max_completion_tokens"].(float64)
			require.Equal(t, float64(tt.expected), maxTokens)
			require.Equal(t, float64(tt.expected), maxCompletionTokens)
		})
	}
}
