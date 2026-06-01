package validation

import (
	"decentralized-api/completionapi"
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
			got := isEmptySentinelTokens(tc.tokens)
			if got != tc.want {
				t.Fatalf("isEmptySentinelTokens() = %v, want %v", got, tc.want)
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
			got := hasNonNumericTokens(tc.tokens)
			if got != tc.want {
				t.Fatalf("hasNonNumericTokens() = %v, want %v", got, tc.want)
			}
		})
	}
}
