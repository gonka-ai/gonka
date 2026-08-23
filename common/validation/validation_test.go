package validation

import (
	"common/completionapi"
	"encoding/json"
	"os"
	"testing"
)

const (
	inferenceJsonPath  = "testdata/inference_response.json"
	validationJsonPath = "testdata/validation_response.json"

	inferenceQuantJsonPath = "testdata/inference_response_int4.json"
	validationFP8tJsonPath = "testdata/validation_response_fp8.json"
)

func loadResponse(path string) (*completionapi.Response, error) {
	response, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r completionapi.Response
	if err := json.Unmarshal(response, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func TestValidation(t *testing.T) {
	inferenceResponse, err := loadResponse(inferenceJsonPath)
	if err != nil {
		t.Fatalf("Failed to read inference response: %v", err)
	}

	validationResponse, err := loadResponse(validationJsonPath)
	if err != nil {
		t.Fatalf("Failed to read validation response: %v", err)
	}

	baseResult := BaseValidationResult{
		InferenceId:   "1",
		ResponseBytes: []byte{},
	}

	val := CompareLogits(inferenceResponse.Choices[0].Logprobs.Content, validationResponse.Choices[0].Logprobs.Content, baseResult)
	t.Logf("Validation result: %v", val)
}

func TestValidationQuant(t *testing.T) {
	inferenceResponse, err := loadResponse(inferenceQuantJsonPath)
	if err != nil {
		t.Fatalf("Failed to read inference response: %v", err)
	}

	validationResponse, err := loadResponse(validationFP8tJsonPath)
	if err != nil {
		t.Fatalf("Failed to read validation response: %v", err)
	}

	baseResult := BaseValidationResult{
		InferenceId:   "1",
		ResponseBytes: []byte{},
	}

	val := CompareLogits(inferenceResponse.Choices[0].Logprobs.Content, validationResponse.Choices[0].Logprobs.Content, baseResult)
	t.Logf("Validation result: %v", val)
}

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

	base := BaseValidationResult{
		InferenceId:   "1",
		ResponseBytes: []byte("test"),
	}

	result := CompareLogits(logits, logits, base)
	if !result.IsSuccessful() {
		t.Fatal("expected matching logits to pass validation")
	}
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

	base := BaseValidationResult{
		InferenceId:   "1",
		ResponseBytes: []byte("test"),
	}

	result := CompareLogits(original, different, base)
	if result.IsSuccessful() {
		t.Fatal("expected different tokens to fail validation")
	}
}

func TestIsEmptySentinelTokens(t *testing.T) {
	cases := []struct {
		name   string
		tokens completionapi.EnforcedTokens
		want   bool
	}{
		{"no sentinel", completionapi.EnforcedTokens{Tokens: []completionapi.EnforcedToken{
			{Token: "42", TopTokens: []string{"1"}},
		}}, false},
		{"sentinel present", completionapi.EnforcedTokens{Tokens: []completionapi.EnforcedToken{
			{Token: "<EMPTY>"},
		}}, true},
		{"sentinel among others", completionapi.EnforcedTokens{Tokens: []completionapi.EnforcedToken{
			{Token: "10"},
			{Token: "<EMPTY>"},
			{Token: "20"},
		}}, true},
		{"empty token list", completionapi.EnforcedTokens{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsEmptySentinelTokens(tc.tokens)
			if got != tc.want {
				t.Fatalf("IsEmptySentinelTokens() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHasNonNumericTokens(t *testing.T) {
	cases := []struct {
		name   string
		tokens completionapi.EnforcedTokens
		want   bool
	}{
		{"valid", completionapi.EnforcedTokens{Tokens: []completionapi.EnforcedToken{
			{Token: "42", TopTokens: []string{"1", "2"}},
		}}, false},
		{"text string", completionapi.EnforcedTokens{Tokens: []completionapi.EnforcedToken{
			{Token: "hello"},
		}}, true},
		{"negative primary", completionapi.EnforcedTokens{Tokens: []completionapi.EnforcedToken{
			{Token: "-1", TopTokens: []string{"3"}},
		}}, true},
		{"negative top token", completionapi.EnforcedTokens{Tokens: []completionapi.EnforcedToken{
			{Token: "42", TopTokens: []string{"-5"}},
		}}, true},
		{"out of range large", completionapi.EnforcedTokens{Tokens: []completionapi.EnforcedToken{
			{Token: "999999999", TopTokens: []string{"1"}},
		}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HasNonNumericTokens(tc.tokens)
			if got != tc.want {
				t.Fatalf("HasNonNumericTokens() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTokenCountInflated(t *testing.T) {
	cases := []struct {
		name       string
		claimed    uint64
		validation uint64
		want       bool
	}{
		{"equal", 100, 100, false},
		{"claimed lower", 90, 100, false},
		{"within tolerance", 100, 98, false},
		{"exactly at tolerance", 100, 97, false},
		{"above tolerance", 100, 96, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TokenCountInflated(tc.claimed, tc.validation); got != tc.want {
				t.Fatalf("TokenCountInflated(%d, %d) = %v, want %v", tc.claimed, tc.validation, got, tc.want)
			}
		})
	}
}
