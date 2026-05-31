package devshard

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"decentralized-api/completionapi"
	"decentralized-api/internal/validation"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	devshardpkg "devshard"
	"devshard/bridge"
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
		"model":               "test-model",
		"messages":            []interface{}{},
		"stream":              true,
		"stream_options":      map[string]interface{}{"include_usage": true},
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

func TestEvaluateValidationResult_UsesModelThreshold(t *testing.T) {
	req := devshardpkg.ValidateRequest{
		EscrowID: "escrow-1",
		EpochID:  7,
		Model:    "model-a",
	}
	resolver := cachedThresholdResolver(req, &bridge.Decimal{Value: 90, Exponent: -2})

	tests := []struct {
		name       string
		similarity float64
		want       bool
	}{
		{name: "above threshold passes", similarity: 0.91, want: true},
		{name: "equal threshold fails", similarity: 0.90, want: false},
		{name: "below threshold fails", similarity: 0.89, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &validation.SimilarityValidationResult{Value: tt.similarity}
			valid, err := EvaluateValidationResult(context.Background(), result, req, resolver)
			require.NoError(t, err)
			assert.Equal(t, tt.want, valid)
		})
	}
}

func TestEvaluateValidationResult_KnownFailureTypesFailWithoutThreshold(t *testing.T) {
	req := devshardpkg.ValidateRequest{}
	results := []validation.ValidationResult{
		&validation.DifferentLengthValidationResult{},
		&validation.DifferentTokensValidationResult{},
		&validation.InvalidInferenceResult{},
	}

	for _, result := range results {
		valid, err := EvaluateValidationResult(context.Background(), result, req, nil)
		require.NoError(t, err)
		assert.False(t, valid)
	}
}

func TestEvaluateValidationResult_UnknownTypeErrors(t *testing.T) {
	valid, err := EvaluateValidationResult(context.Background(), unknownValidationResult{}, devshardpkg.ValidateRequest{}, nil)
	require.Error(t, err)
	assert.False(t, valid)
}

func TestValidationThresholdResolverFailsClosed(t *testing.T) {
	resolver := NewValidationThresholdResolver(nil, time.Minute)
	_, err := resolver.Resolve(context.Background(), "escrow-1", 7, "model-a")
	require.Error(t, err)

	resolver = NewValidationThresholdResolver(&thresholdBridge{}, time.Minute)
	_, err = resolver.Resolve(context.Background(), "escrow-1", 7, "model-a")
	require.Error(t, err)
}

func cachedThresholdResolver(req devshardpkg.ValidateRequest, threshold *bridge.Decimal) *ValidationThresholdResolver {
	return &ValidationThresholdResolver{
		cache: map[validationThresholdCacheKey]validationThresholdCacheEntry{
			{escrowID: req.EscrowID, modelID: req.Model}: {
				epochID:   req.EpochID,
				threshold: threshold,
				expiresAt: time.Now().Add(time.Hour),
			},
		},
	}
}

type unknownValidationResult struct{}

func (unknownValidationResult) IsSuccessful() bool { return true }

func (unknownValidationResult) GetInferenceId() string { return "unknown" }

func (unknownValidationResult) GetValidationResponseBytes() []byte { return nil }

type thresholdBridge struct{}

func (thresholdBridge) GetEscrow(string) (*bridge.EscrowInfo, error) {
	return nil, bridge.ErrNotImplemented
}

func (thresholdBridge) GetHostInfo(string) (*bridge.HostInfo, error) {
	return nil, bridge.ErrNotImplemented
}

func (thresholdBridge) GetValidationThreshold(uint64, string) (*bridge.Decimal, error) {
	return nil, nil
}

func (thresholdBridge) VerifyWarmKey(string, string) (bool, error) {
	return false, bridge.ErrNotImplemented
}

func (thresholdBridge) OnEscrowCreated(bridge.EscrowInfo) error { return bridge.ErrNotImplemented }

func (thresholdBridge) OnSettlementProposed(string, []byte, uint64) error {
	return bridge.ErrNotImplemented
}

func (thresholdBridge) OnSettlementFinalized(string) error { return bridge.ErrNotImplemented }

func (thresholdBridge) SubmitDisputeState(string, []byte, uint64, map[uint32][]byte) error {
	return bridge.ErrNotImplemented
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

func TestTokenCountValidation_AllowsTokenDriftUpToThreshold(t *testing.T) {
	const tokenCountDriftThreshold = 3
	assert.False(t, tokenCountInflated(10+tokenCountDriftThreshold-1, 10), "drift below threshold should be tolerated")
	assert.False(t, tokenCountInflated(10, 10), "matching token count should pass")
	assert.False(t, tokenCountInflated(9, 10), "lower claimed token count should pass")
	assert.False(t, tokenCountInflated(10+tokenCountDriftThreshold, 10), "drift at threshold should be accepted")
	assert.True(t, tokenCountInflated(10+tokenCountDriftThreshold+1, 10), "drift above threshold should be rejected")
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
