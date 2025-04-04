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
