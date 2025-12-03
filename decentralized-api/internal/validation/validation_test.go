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

	val := compareLogits(inferenceResponse.Choices[0].Logprobs.Content, validationResponse.Choices[0].Logprobs.Content, baseResult)
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

	val := compareLogits(inferenceResponse.Choices[0].Logprobs.Content, validationResponse.Choices[0].Logprobs.Content, baseResult)
	t.Logf("Validation result: %v", val)
}

func TestSequenceCheckValid(t *testing.T) {
	runSeed := "test_seed_12345"

	enforcedTokens := completionapi.EnforcedTokens{
		RunSeed: runSeed,
		Tokens: []completionapi.EnforcedToken{
			{Token: "Hello", TopTokens: []string{"Hello", "Hi", "Hey"}},
			{Token: "world", TopTokens: []string{"world", "universe", "earth"}},
		},
	}

	logits := []completionapi.Logprob{
		{Token: "Hello"},
		{Token: "world"},
	}

	err := checkSequenceFromArtifact(enforcedTokens, runSeed, logits)
	if err == nil {
		t.Fatal("Expected sequence check to detect mismatch between RNG sampling and actual tokens")
	}
}

func TestSequenceCheckEmptySeed(t *testing.T) {
	enforcedTokens := completionapi.EnforcedTokens{
		RunSeed: "",
		Tokens: []completionapi.EnforcedToken{
			{Token: "Hello", TopTokens: []string{"Hello", "Hi"}},
		},
	}

	logits := []completionapi.Logprob{
		{Token: "Hello"},
	}

	err := checkSequenceFromArtifact(enforcedTokens, "", logits)
	if err != nil {
		t.Fatalf("Empty seed should skip validation: %v", err)
	}
}

func TestSequenceCheckLengthMismatch(t *testing.T) {
	runSeed := "test_seed"

	enforcedTokens := completionapi.EnforcedTokens{
		RunSeed: runSeed,
		Tokens: []completionapi.EnforcedToken{
			{Token: "Hello", TopTokens: []string{"Hello"}},
		},
	}

	logits := []completionapi.Logprob{
		{Token: "Hello"},
		{Token: "world"},
	}

	err := checkSequenceFromArtifact(enforcedTokens, runSeed, logits)
	if err == nil {
		t.Fatal("Expected error for length mismatch")
	}
}
