package validation

import (
	"context"
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

	sequenceCheckValid1Path    = "testdata/sequence_check_valid.json"
	sequenceCheckValid2Path    = "testdata/sequence_check_valid2.json"
	DistributionCheckValidPath = "testdata/distribution_check_valid.json"
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

func TestSequenceCheckValidTokensInTopK(t *testing.T) {
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
	if err != nil {
		t.Fatalf("Sequence check should pass when all tokens are in top_k: %v", err)
	}
}

func TestSequenceCheckInvalidTokenNotInTopK(t *testing.T) {
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
		{Token: "INVALID"},
	}

	err := checkSequenceFromArtifact(enforcedTokens, runSeed, logits)
	if err == nil {
		t.Fatal("Expected sequence check to fail when token not in top_k")
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

func TestSequenceCheckRealData1(t *testing.T) {
	response, err := loadResponse(sequenceCheckValid1Path)
	if err != nil {
		t.Fatalf("Failed to load test response: %v", err)
	}

	if len(response.Choices) == 0 {
		t.Fatal("No choices in response")
	}

	choice := response.Choices[0]
	runSeed := choice.Logprobs.RunSeed

	if runSeed == "" {
		t.Skip("No run_seed in test data, skipping sequence check test")
	}

	var enforcedTokens completionapi.EnforcedTokens
	enforcedTokens.RunSeed = runSeed

	for _, logprob := range choice.Logprobs.Content {
		var topTokens []string
		for _, topLogprob := range logprob.TopLogprobs {
			topTokens = append(topTokens, topLogprob.Token)
		}
		enforcedTokens.Tokens = append(enforcedTokens.Tokens, completionapi.EnforcedToken{
			Token:     logprob.Token,
			TopTokens: topTokens,
		})
	}

	err = checkSequenceFromArtifact(enforcedTokens, runSeed, choice.Logprobs.Content)
	if err != nil {
		t.Errorf("Sequence check failed for valid data: %v", err)
		t.Logf("run_seed: %s", runSeed)
		t.Logf("Token count: %d", len(choice.Logprobs.Content))
		if len(choice.Logprobs.Content) > 0 {
			t.Logf("First token: %s, top_tokens: %v",
				choice.Logprobs.Content[0].Token,
				enforcedTokens.Tokens[0].TopTokens)
		}
	} else {
		t.Logf("Sequence check passed for %d tokens", len(choice.Logprobs.Content))
	}
}

func TestSequenceCheckRealData2(t *testing.T) {
	response, err := loadResponse(sequenceCheckValid2Path)
	if err != nil {
		t.Fatalf("Failed to load test response: %v", err)
	}

	if len(response.Choices) == 0 {
		t.Fatal("No choices in response")
	}

	choice := response.Choices[0]
	runSeed := choice.Logprobs.RunSeed

	if runSeed == "" {
		t.Skip("No run_seed in test data, skipping sequence check test")
	}

	var enforcedTokens completionapi.EnforcedTokens
	enforcedTokens.RunSeed = runSeed

	for _, logprob := range choice.Logprobs.Content {
		var topTokens []string
		for _, topLogprob := range logprob.TopLogprobs {
			topTokens = append(topTokens, topLogprob.Token)
		}
		enforcedTokens.Tokens = append(enforcedTokens.Tokens, completionapi.EnforcedToken{
			Token:     logprob.Token,
			TopTokens: topTokens,
		})
	}

	err = checkSequenceFromArtifact(enforcedTokens, runSeed, choice.Logprobs.Content)
	if err != nil {
		t.Errorf("Sequence check failed for valid data: %v", err)
		t.Logf("run_seed: %s", runSeed)
		t.Logf("Token count: %d", len(choice.Logprobs.Content))
	} else {
		t.Logf("Sequence check passed for %d tokens", len(choice.Logprobs.Content))
	}
}

func TestSequenceCheckTamperedToken(t *testing.T) {
	response, err := loadResponse(sequenceCheckValid1Path)
	if err != nil {
		t.Fatalf("Failed to load test response: %v", err)
	}

	if len(response.Choices) == 0 || len(response.Choices[0].Logprobs.Content) < 2 {
		t.Fatal("Insufficient data in response")
	}

	choice := response.Choices[0]
	runSeed := choice.Logprobs.RunSeed

	if runSeed == "" {
		t.Skip("No run_seed in test data, skipping test")
	}

	var enforcedTokens completionapi.EnforcedTokens
	enforcedTokens.RunSeed = runSeed

	for _, logprob := range choice.Logprobs.Content {
		var topTokens []string
		for _, topLogprob := range logprob.TopLogprobs {
			topTokens = append(topTokens, topLogprob.Token)
		}
		enforcedTokens.Tokens = append(enforcedTokens.Tokens, completionapi.EnforcedToken{
			Token:     logprob.Token,
			TopTokens: topTokens,
		})
	}

	tamperedLogits := make([]completionapi.Logprob, len(choice.Logprobs.Content))
	copy(tamperedLogits, choice.Logprobs.Content)
	tamperedLogits[1].Token = "TAMPERED_TOKEN_12345"

	err = checkSequenceFromArtifact(enforcedTokens, runSeed, tamperedLogits)
	if err == nil {
		t.Error("Expected sequence check to fail with tampered token, but it passed")
	}
}

func TestSequenceCheckWrongSeed(t *testing.T) {
	response1, err := loadResponse(sequenceCheckValid1Path)
	if err != nil {
		t.Fatalf("Failed to load first test response: %v", err)
	}

	response2, err := loadResponse(sequenceCheckValid2Path)
	if err != nil {
		t.Fatalf("Failed to load second test response: %v", err)
	}

	if len(response1.Choices) == 0 || len(response2.Choices) == 0 {
		t.Fatal("Missing choices in responses")
	}

	choice1 := response1.Choices[0]
	choice2 := response2.Choices[0]

	if choice1.Logprobs.RunSeed == "" || choice2.Logprobs.RunSeed == "" {
		t.Skip("No run_seed in test data, skipping test")
	}

	var enforcedTokens completionapi.EnforcedTokens
	enforcedTokens.RunSeed = choice1.Logprobs.RunSeed

	for _, logprob := range choice1.Logprobs.Content {
		var topTokens []string
		for _, topLogprob := range logprob.TopLogprobs {
			topTokens = append(topTokens, topLogprob.Token)
		}
		enforcedTokens.Tokens = append(enforcedTokens.Tokens, completionapi.EnforcedToken{
			Token:     logprob.Token,
			TopTokens: topTokens,
		})
	}

	err = checkSequenceFromArtifact(enforcedTokens, choice2.Logprobs.RunSeed, choice1.Logprobs.Content)
	if err != nil {
		t.Logf("Sequence check correctly rejected mismatched run_seed")
	}
}

func TestDistributionCheckWithVLLM(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping distribution check test in short mode")
	}

	vllmURL := "http://localhost:8080"
	validatorClient := NewValidatorClient(vllmURL)

	response, err := loadResponse(DistributionCheckValidPath)
	if err != nil {
		t.Fatalf("Failed to load test response: %v", err)
	}
	if len(response.Choices) == 0 {
		t.Fatal("No choices in response")
	}
	choice1 := response.Choices[0]

	model := response.Model
	if model == "" {
		t.Fatal("No model in response")
	}

	requestMap := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": "Who won the world series in 2020? Generate a funny and original text.",
			},
		},
		"max_tokens":   80,
		"temperature":  0.5,
		"seed":         40,
		"run_seed":     choice1.Logprobs.RunSeed,
		"stream":       false,
		"logprobs":     1,
		"top_logprobs": 3,
	}

	t.Logf("Testing distribution check with model: %s", model)
	t.Logf("vLLM URL: %s", vllmURL)

	modelConfig := SetModelRequest{
		Model:          model,
		Dtype:          "auto",
		AdditionalArgs: []string{"--max-model-len", "2048", "--gpu-memory-utilization", "0.5", "--chat-template \"<|im_start|>{{ role }}\\n{{ content }}<|im_end|>\""},
	}

	ctx := context.Background()

	t.Log("Running first inference...")
	statusResp1, err := validatorClient.PerformInferenceWithSetup(ctx, modelConfig, requestMap)
	if err != nil {
		t.Fatalf("Failed to perform first inference with vLLM setup: %v", err)
	}

	t.Logf("First inference completed with status: %s", statusResp1.Status)

	if err := validatorClient.StopVLLM(ctx); err != nil {
		t.Logf("Warning: Failed to stop vLLM after first inference: %v", err)
	}

	logits1, err := extractLogitsFromValidatorResult(statusResp1.Result)
	t.Logf("DEBUG: logits1=%+v, err=%v", statusResp1.Result, err)
	if err != nil {
		t.Fatalf("Failed to extract logits from first result: %v", err)
	}

	t.Logf("Extracted %d tokens from first inference", len(logits1))
	if len(logits1) > 0 {
		t.Logf("First token: %s (logprob: %.4f, top_logprobs: %d)",
			logits1[0].Token, logits1[0].Logprob, len(logits1[0].TopLogprobs))
	}

	t.Log("Running second inference with same parameters...")
	statusResp2, err := validatorClient.PerformInferenceWithSetup(ctx, modelConfig, requestMap)
	if err != nil {
		t.Fatalf("Failed to perform second inference with vLLM setup: %v", err)
	}

	t.Logf("Second inference completed with status: %s", statusResp2.Status)

	if err := validatorClient.StopVLLM(ctx); err != nil {
		t.Logf("Warning: Failed to stop vLLM after second inference: %v", err)
	}

	logits2, err := extractLogitsFromValidatorResult(statusResp2.Result)
	if err != nil {
		t.Fatalf("Failed to extract logits from second result: %v", err)
	}

	t.Logf("Extracted %d tokens from second inference", len(logits2))

	fileLogits := response.Choices[0].Logprobs.Content
	t.Logf("Extracted %d tokens from file response", len(fileLogits))

	baseResult := BaseValidationResult{
		InferenceId:   "test-distribution-check",
		ResponseBytes: []byte{},
	}

	t.Log("\n=== Comparing vLLM inference 1 vs inference 2 ===")
	result := compareDistributions(logits1, logits2, baseResult)
	t.Logf("Distribution comparison result: %T", result)

	if similarityResult, ok := result.(*SimilarityValidationResult); ok {
		t.Logf("Similarity score (vLLM 1 vs 2): %.6f", similarityResult.Value)
		t.Logf("Is successful (>0.99): %v", similarityResult.IsSuccessful())

		if !similarityResult.IsSuccessful() {
			t.Fatalf("vLLM inference 1 vs 2 comparison failed: similarity %.6f is below threshold", similarityResult.Value)
		}
	} else {
		t.Fatalf("Expected SimilarityValidationResult, got %T", result)
	}

	t.Log("\n=== Comparing vLLM inference 1 vs file response ===")
	resultVsFile1 := compareDistributions(logits1, fileLogits, baseResult)
	if similarityResult, ok := resultVsFile1.(*SimilarityValidationResult); ok {
		t.Logf("Similarity score (vLLM 1 vs file): %.6f", similarityResult.Value)
		if !similarityResult.IsSuccessful() {
			t.Fatalf("vLLM inference 1 vs file comparison failed: similarity %.6f is below threshold", similarityResult.Value)
		}
	} else {
		t.Fatalf("Expected SimilarityValidationResult for vLLM 1 vs file, got %T", resultVsFile1)
	}

	t.Log("\n=== Comparing vLLM inference 2 vs file response ===")
	resultVsFile2 := compareDistributions(logits2, fileLogits, baseResult)
	if similarityResult, ok := resultVsFile2.(*SimilarityValidationResult); ok {
		t.Logf("Similarity score (vLLM 2 vs file): %.6f", similarityResult.Value)
		if !similarityResult.IsSuccessful() {
			t.Fatalf("vLLM inference 2 vs file comparison failed: similarity %.6f is below threshold", similarityResult.Value)
		}
	} else {
		t.Fatalf("Expected SimilarityValidationResult for vLLM 2 vs file, got %T", resultVsFile2)
	}

	if len(logits1) > 0 && len(logits2) > 0 {
		t.Log("\nFirst 3 tokens comparison:")
		for i := 0; i < 3 && i < len(logits1) && i < len(logits2); i++ {
			t.Logf("  Position %d:", i)
			t.Logf("    vLLM 1: %s (%.4f)", logits1[i].Token, logits1[i].Logprob)
			t.Logf("    vLLM 2: %s (%.4f)", logits2[i].Token, logits2[i].Logprob)
			if i < len(fileLogits) {
				t.Logf("    File:   %s (%.4f)", fileLogits[i].Token, fileLogits[i].Logprob)
			}
		}
	}
}
