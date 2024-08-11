package main

import (
	"bytes"
	"context"
	"decentralized-api/broker"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"inference/api/inference/inference"
	"inference/x/inference/types"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
)

func SampleInferenceToValidate(ids []string, transactionRecorder InferenceCosmosClient, nodeBroker *broker.Broker) {
	log.Printf("Sampling inf transactions to validate")

	queryClient := transactionRecorder.NewInferenceQueryClient()

	r, err := queryClient.GetInferencesWithExecutors(transactionRecorder.context, &types.QueryGetInferencesWithExecutorsRequest{Ids: ids})
	if err != nil {
		// FIXME: what should we do with validating the transaction?
		log.Printf("Failed to query GetInferencesWithExecutors")
		return
	}

	log.Printf("Inferences to validate: %v", r.InferenceWithExecutor)

	var toValidate []types.Inference
	for _, inferenceWithExecutor := range r.InferenceWithExecutor {
		if shouldValidate(inferenceWithExecutor.Executor, transactionRecorder.address, r.NumValidators) {
			toValidate = append(toValidate, inferenceWithExecutor.Inference)
		}
	}

	for _, inf := range toValidate {
		go func() {
			validateInferenceAndSendValMessage(inf, nodeBroker, transactionRecorder)
		}()
	}
}

func shouldValidate(executor types.Participant, currentAccountAddress string, numValidators uint32) bool {
	// Don't validate your own transactions
	if executor.Index == currentAccountAddress {
		return false
	}

	if numValidators <= 1 {
		return true
	}

	reputationP := getReputationP(executor.Status)
	samplingP := 1 - math.Pow(1-reputationP, 1/float64(numValidators-1))

	log.Printf("reputationP = %v. samplingP = %v", reputationP, samplingP)

	return rand.Float64() < samplingP
}

func getReputationP(status types.ParticipantStatus) float64 {
	switch status {
	case types.ParticipantStatus_ACTIVE:
		return 0.95
	default:
		return 0.1
	}
}

func validateInferenceAndSendValMessage(inf types.Inference, nodeBroker *broker.Broker, transactionRecorder InferenceCosmosClient) {
	valResult, err := lockNodeAndValidate(inf, nodeBroker)
	if err != nil {
		log.Printf("Failed to validate inf. id = %v. err = %v", inf.InferenceId, err)
		return
	}

	msgValidation, err := ToMsgValidation(valResult)
	if err != nil {
		log.Printf("Failed to convert to MsgValidation. id = %v. err = %v", inf.InferenceId, err)
		return
	}

	if err = transactionRecorder.ReportValidation(msgValidation); err != nil {
		log.Printf("Failed to report validation. id = %v. err = %v", inf.InferenceId, err)
		return
	}

	log.Printf("Successfully validated inference. id = %v", inf.InferenceId)
}

func ValidateByInferenceId(id string, node *broker.InferenceNode, transactionRecorder InferenceCosmosClient) (ValidationResult, error) {
	queryClient := transactionRecorder.NewInferenceQueryClient()
	r, err := queryClient.Inference(context.Background(), &types.QueryGetInferenceRequest{Index: id})
	if err != nil {
		log.Printf("Failed get inference by id query. id = %s. err = %v", id, err)
	}

	return validate(r.Inference, node)
}

func lockNodeAndValidate(inference types.Inference, nodeBroker *broker.Broker) (ValidationResult, error) {
	return broker.LockNode(nodeBroker, testModel, func(node *broker.InferenceNode) (ValidationResult, error) {
		return validate(inference, node)
	})
}

func validate(inference types.Inference, inferenceNode *broker.InferenceNode) (ValidationResult, error) {
	if inference.Status != types.InferenceStatus_FINISHED {
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

func ToMsgValidation(result ValidationResult) (*inference.MsgValidation, error) {
	// Match type of result from implementations of ValidationResult
	var cosineSimVal float64
	switch result.(type) {
	case *DifferentLengthValidationResult:
		log.Printf("Different length validation result")
		cosineSimVal = -1
	case *DifferentTokensValidationResult:
		log.Printf("Different tokens validation result")
		cosineSimVal = -1
	case *CosineSimilarityValidationResult:
		log.Printf("Cosine similarity validation result")
		cosineSimVal = result.(*CosineSimilarityValidationResult).Value
	default:
		return nil, errors.New("unknown validation result type")
	}

	responseHash, _, err := getResponseHash(result.GetValidationResponseBytes())
	if err != nil {
		return nil, err
	}

	return &inference.MsgValidation{
		Id:              uuid.New().String(),
		InferenceId:     result.GetInferenceId(),
		ResponsePayload: string(result.GetValidationResponseBytes()),
		ResponseHash:    responseHash,
		Value:           cosineSimVal,
	}, nil
}
