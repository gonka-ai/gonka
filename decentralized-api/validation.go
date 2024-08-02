package main

import (
	"bytes"
	"context"
	"decentralized-api/broker"
	"encoding/json"
	"errors"
	"google.golang.org/grpc"
	"inference/x/inference/types"
	"io"
	"log"
	"math"
	"net/http"
	"time"
)

func StartValidationScheduledTask(transactionRecorder InferenceCosmosClient, config Config) {
	// Sleep but every X seconds wake up and do the task
	for {
		time.Sleep(5 * time.Second)
		// TODO: query transaction
		conn, err := grpc.NewClient(config.ChainNode.Url)
		if err != nil {
			log.Printf("Error creating grpc client: %v", err)
			continue
		}

		queryClient := types.NewQueryClient(conn)
		r, err := queryClient.Inference(context.Background(), &types.QueryGetInferenceRequest{Index: "1"})
		if err != nil {
			log.Printf("Failed to query a transaction for validation	: %v", err)
		}
		log.Printf("Inference to validate: %v", r.Inference)
		//validate(r.Inference)
	}
}

func ValidateByInferenceId(id string, node *broker.InferenceNode, transactionRecorder InferenceCosmosClient) (ValidationResult, error) {
	queryClient := types.NewQueryClient(transactionRecorder.client.Context())
	r, err := queryClient.Inference(context.Background(), &types.QueryGetInferenceRequest{Index: id})
	if err != nil {
		log.Printf("Failed get inference by id query. id = %s. err = %v", id, err)
	}

	return validate(r.Inference, node)
}

func validate(inference types.Inference, inferenceNode *broker.InferenceNode) (ValidationResult, error) {
	if inference.Status != "FINISHED" {
		return nil, errors.New("Inference is not finished. id = " + inference.InferenceId)
	}

	var requestMap map[string]interface{}
	if err := json.Unmarshal([]byte(inference.PromptPayload), &requestMap); err != nil {
		log.Printf("Failed to unmarshal inference.PromptPayload. id = %v. err = %v", inference.InferenceId, err)
		return nil, err
	}

	var originalResponse broker.Response
	if err := json.Unmarshal([]byte(inference.ResponsePayload), &originalResponse); err != nil {
		log.Printf("Failed to unmarshal inference.ResponsePayload. id = %v. err = %v", inference.InferenceId, err)
		return nil, err
	}

	//goland:noinspection GoDfaNilDereference
	requestMap["enforced_str"] = originalResponse.Choices[0].Message.Content

	// Serialize requestMap to JSON
	requestBody, err := json.Marshal(requestMap)
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(
		inferenceNode.Url+"v1/chat/completions",
		"application/json",
		bytes.NewReader(requestBody),
	)
	if err != nil {
		return nil, err
	}

	respBodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	log.Printf("responseValidation = %v", string(respBodyBytes))
	var responseValidation broker.Response
	if err = json.Unmarshal(respBodyBytes, &responseValidation); err != nil {
		return nil, err
	}

	originalLogits := extractLogits(originalResponse)
	validationLogits := extractLogits(responseValidation)
	baseResult := BaseValidationResult{
		InferenceId:   inference.InferenceId,
		ResponseBytes: respBodyBytes,
	}

	return compareLogits(originalLogits, validationLogits, baseResult), nil
}

func extractLogits(response broker.Response) []broker.Logprob {
	var logits []broker.Logprob
	// Concatenate all logrpobs
	for _, c := range response.Choices {
		logits = append(logits, c.Logprobs.Content...)
	}
	return logits
}

type ValidationResult interface {
	GetInferenceId() string

	GetValidationResponseBytes() []byte

	IsSuccessful() bool
}

type BaseValidationResult struct {
	InferenceId   string
	ResponseBytes []byte
}

func (r BaseValidationResult) GetInferenceId() string {
	return r.InferenceId
}

func (r BaseValidationResult) GetValidationResponseBytes() []byte {
	return r.ResponseBytes
}

type DifferentLengthValidationResult struct {
	BaseValidationResult
}

func (DifferentLengthValidationResult) IsSuccessful() bool {
	return false
}

type DifferentTokensValidationResult struct {
	BaseValidationResult
}

func (DifferentTokensValidationResult) IsSuccessful() bool {
	return false
}

type CosineSimilarityValidationResult struct {
	BaseValidationResult
	Value float64
}

func (r CosineSimilarityValidationResult) IsSuccessful() bool {
	return r.Value > 0.99
}

func compareLogits(
	originalLogits []broker.Logprob,
	validationLogits []broker.Logprob,
	baseComparisonResult BaseValidationResult,
) ValidationResult {
	if len(originalLogits) != len(validationLogits) {
		return DifferentLengthValidationResult{baseComparisonResult}
	}

	var originalLogprobs, validationLogprobs []float64
	for i := range originalLogits {
		o := originalLogits[i]
		v := validationLogits[i]
		if o.Token != v.Token {
			return DifferentTokensValidationResult{baseComparisonResult}
		}

		originalLogprobs = append(originalLogprobs, o.Logprob)
		validationLogprobs = append(validationLogprobs, v.Logprob)
	}

	cosSimValue := cosineSimilarity(originalLogprobs, validationLogprobs)

	return &CosineSimilarityValidationResult{BaseValidationResult: baseComparisonResult, Value: cosSimValue}
}

func cosineSimilarity(a, b []float64) float64 {
	// TODO: handle division by zero case
	var dotProduct, magnitudeA, magnitudeB float64
	for i := range a {
		dotProduct += a[i] * b[i]
		magnitudeA += a[i] * a[i]
		magnitudeB += b[i] * b[i]
	}
	return dotProduct / (math.Sqrt(magnitudeA) * math.Sqrt(magnitudeB))
}
