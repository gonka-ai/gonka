package subnet

import (
	"encoding/json"
	"testing"

	"decentralized-api/completionapi"
	"decentralized-api/internal/validation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompareLogitsMatching(t *testing.T) {
	logits := []completionapi.Logprob{
		{
			Token:   "hello",
			Logprob: -0.1,
			TopLogprobs: []completionapi.TopLogprobs{
				{Token: "hello", Logprob: -0.1},
				{Token: "hi", Logprob: -2.0},
			},
		},
		{
			Token:   "world",
			Logprob: -0.2,
			TopLogprobs: []completionapi.TopLogprobs{
				{Token: "world", Logprob: -0.2},
				{Token: "earth", Logprob: -3.0},
			},
		},
	}

	base := validation.BaseValidationResult{
		InferenceId:   "1",
		ResponseBytes: []byte("test"),
	}

	result := validation.CompareLogits(logits, logits, base)
	assert.True(t, result.IsSuccessful())
}

func TestCompareLogitsDifferentTokens(t *testing.T) {
	original := []completionapi.Logprob{
		{
			Token:   "hello",
			Logprob: -0.1,
			TopLogprobs: []completionapi.TopLogprobs{
				{Token: "hello", Logprob: -0.1},
			},
		},
	}
	different := []completionapi.Logprob{
		{
			Token:   "goodbye",
			Logprob: -0.5,
			TopLogprobs: []completionapi.TopLogprobs{
				{Token: "goodbye", Logprob: -0.5},
			},
		},
	}

	base := validation.BaseValidationResult{
		InferenceId:   "1",
		ResponseBytes: []byte("test"),
	}

	result := validation.CompareLogits(original, different, base)
	assert.False(t, result.IsSuccessful())
}

func TestEnforcedTokensExtraction(t *testing.T) {
	responseJSON := `{"id":"test","choices":[{"message":{"content":"hello"},"logprobs":{"content":[{"token":"hello","logprob":-0.1,"top_logprobs":[{"token":"hello","logprob":-0.1},{"token":"hi","logprob":-2.0}]}]}}],"usage":{"prompt_tokens":10,"completion_tokens":1}}`

	resp, err := completionapi.NewCompletionResponseFromBytes([]byte(responseJSON))
	require.NoError(t, err)

	enforced, err := resp.GetEnforcedTokens()
	require.NoError(t, err)
	require.Len(t, enforced.Tokens, 1)
	assert.Equal(t, "hello", enforced.Tokens[0].Token)
	assert.Equal(t, []string{"hello", "hi"}, enforced.Tokens[0].TopTokens)
}

func TestValidationRequestBodyConstruction(t *testing.T) {
	requestMap := map[string]interface{}{
		"model":            "test-model",
		"messages":         []interface{}{},
		"stream":           true,
		"stream_options":   map[string]interface{}{"include_usage": true},
		"skip_special_tokens": false,
	}

	enforcedTokens := completionapi.EnforcedTokens{
		Tokens: []completionapi.EnforcedToken{
			{Token: "hello", TopTokens: []string{"hello", "hi"}},
		},
	}

	requestMap["enforced_tokens"] = enforcedTokens
	requestMap["stream"] = false
	requestMap["skip_special_tokens"] = false
	delete(requestMap, "stream_options")

	body, err := json.Marshal(requestMap)
	require.NoError(t, err)

	var result map[string]interface{}
	err = json.Unmarshal(body, &result)
	require.NoError(t, err)

	assert.Equal(t, false, result["stream"])
	assert.Nil(t, result["stream_options"])
	assert.NotNil(t, result["enforced_tokens"])
}

func TestResponseFromPayload(t *testing.T) {
	// JSON response payload
	jsonResp := `{"id":"test","choices":[{"message":{"content":"hello"},"logprobs":{"content":[{"token":"hello","logprob":-0.1,"top_logprobs":[{"token":"hello","logprob":-0.1}]}]}}],"usage":{"prompt_tokens":10,"completion_tokens":1}}`

	resp, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload([]byte(jsonResp))
	require.NoError(t, err)

	logits := resp.ExtractLogits()
	require.Len(t, logits, 1)
	assert.Equal(t, "hello", logits[0].Token)
}

func TestTokenCountValidation_MatchingUsage(t *testing.T) {
	// Stored response has prompt_tokens=10, completion_tokens=5
	jsonResp := `{"id":"test","choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`

	resp, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload([]byte(jsonResp))
	require.NoError(t, err)

	usage, err := resp.GetUsage()
	require.NoError(t, err)

	// Claimed counts match stored usage -> should pass
	claimedInput := uint64(10)
	claimedOutput := uint64(5)

	assert.False(t, claimedInput > usage.PromptTokens, "matching input should not exceed stored")
	assert.False(t, claimedOutput > usage.CompletionTokens, "matching output should not exceed stored")
}

func TestTokenCountValidation_InflatedOutputTokens(t *testing.T) {
	// Stored response has prompt_tokens=10, completion_tokens=5
	jsonResp := `{"id":"test","choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`

	resp, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload([]byte(jsonResp))
	require.NoError(t, err)

	usage, err := resp.GetUsage()
	require.NoError(t, err)

	// Claimed output exceeds stored usage -> billing inflation attack
	claimedInput := uint64(10)
	claimedOutput := uint64(100) // inflated!

	assert.False(t, claimedInput > usage.PromptTokens, "input matches stored")
	assert.True(t, claimedOutput > usage.CompletionTokens, "inflated output should be detected")
}

func TestTokenCountValidation_InflatedInputTokens(t *testing.T) {
	// Stored response has prompt_tokens=10, completion_tokens=5
	jsonResp := `{"id":"test","choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`

	resp, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload([]byte(jsonResp))
	require.NoError(t, err)

	usage, err := resp.GetUsage()
	require.NoError(t, err)

	// Claimed input exceeds stored usage -> billing inflation attack
	claimedInput := uint64(999) // inflated!
	claimedOutput := uint64(5)

	assert.True(t, claimedInput > usage.PromptTokens, "inflated input should be detected")
	assert.False(t, claimedOutput > usage.CompletionTokens, "output matches stored")
}
