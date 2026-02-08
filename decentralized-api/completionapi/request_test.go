package completionapi

import (
	"encoding/json"
	"log"
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
	log.Print(string(r.NewBody))
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
	log.Print(string(r.NewBody))
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
	log.Print(string(r.NewBody))
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

func TestSeedIsSetForReproducibleSampling(t *testing.T) {
	// Test that seed is set when not provided in request
	r, err := ModifyRequestBody([]byte(jsonBody), 42)
	require.NoError(t, err)
	require.NotNil(t, r)

	var requestMap map[string]interface{}
	err = json.Unmarshal(r.NewBody, &requestMap)
	require.NoError(t, err, "failed to unmarshal request body")

	// Verify seed is set to the default value
	seed := requestMap["seed"].(float64)
	require.Equal(t, float64(42), seed, "seed should be set for reproducible sampling")
}

func TestSeedIsPreservedWhenProvided(t *testing.T) {
	// Test that user-provided seed is preserved
	jsonBodyWithSeed := `{
		"temperature": 0.8,
		"model": "Qwen/Qwen2.5-7B-Instruct",
		"seed": 12345,
		"messages": [{"role": "user", "content": "Hi!"}]
	}`

	r, err := ModifyRequestBody([]byte(jsonBodyWithSeed), 42)
	require.NoError(t, err)
	require.NotNil(t, r)

	var requestMap map[string]interface{}
	err = json.Unmarshal(r.NewBody, &requestMap)
	require.NoError(t, err, "failed to unmarshal request body")

	// Verify user-provided seed is preserved (not overwritten by default)
	seed := requestMap["seed"].(float64)
	require.Equal(t, float64(12345), seed, "user-provided seed should be preserved")
}
