package main

import (
	"bytes"
	"context"
	"decentralized-api/broker"
	"decentralized-api/completionapi"
	cosmosclient "decentralized-api/cosmosclient"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"io"
	"log"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"strings"
)

func VerifyInvalidation(events map[string][]string, recorder cosmosclient.InferenceCosmosClient, nodeBroker *broker.Broker) {
	inferenceIds, ok := events["inference_validation.inference_id"]
	if !ok || len(inferenceIds) == 0 {
		slog.Error("Validation: No inference_id found in events")
		return
	}
	inferenceId := inferenceIds[0]

	slog.Debug("Validation: Verifying invalidation", "inference_id", inferenceId)

	queryClient := recorder.NewInferenceQueryClient()

	r, err := queryClient.GetInferencesWithExecutors(recorder.Context, &types.QueryGetInferencesWithExecutorsRequest{Ids: []string{inferenceId}})
	if err != nil {
		// FIXME: what should we do with validating the transaction?
		slog.Warn("Validation: Failed to query GetInferencesWithExecutors for revalidation.", "error", err)
		return
	}
	var toValidate []types.Inference
	for _, inferenceWithExecutor := range r.InferenceWithExecutor {
		toValidate = append(toValidate, inferenceWithExecutor.Inference)
	}

	logInferencesToValidate(toValidate)

	for _, inf := range toValidate {
		go func() {
			validateInferenceAndSendValMessage(inf, nodeBroker, recorder, true)
		}()
	}
}

func SampleInferenceToValidate(ids []string, transactionRecorder cosmosclient.InferenceCosmosClient, nodeBroker *broker.Broker) {
	if ids == nil {
		slog.Debug("Validation: No inferences to validate")
		return
	}

	slog.Debug("Validation: Sampling inf transactions to validate")

	queryClient := transactionRecorder.NewInferenceQueryClient()

	query := &types.QueryGetInferencesWithExecutorsRequest{
		Ids:       ids,
		Requester: transactionRecorder.Address,
	}
	r, err := queryClient.GetInferencesWithExecutors(transactionRecorder.Context, query)
	if err != nil {
		// FIXME: what should we do with validating the transaction?
		slog.Warn("Validation: Failed to query GetInferencesWithExecutors.", "error", err)
		return
	}

	logInferencesToSample(r.InferenceWithExecutor)

	var toValidate []types.Inference
	for _, inferenceWithExecutor := range r.InferenceWithExecutor {

		shouldValidate, _ := ShouldValidate(&inferenceWithExecutor.Executor, transactionRecorder.Address,
			r.ValidatorPower, r.TotalPower-inferenceWithExecutor.CurrentPower)
		if shouldValidate {
			toValidate = append(toValidate, inferenceWithExecutor.Inference)
		}
	}

	logInferencesToValidate(toValidate)
	for _, inf := range toValidate {
		go func() {
			validateInferenceAndSendValMessage(inf, nodeBroker, transactionRecorder, false)
		}()
	}
}

func logInferencesToSample(inferences []types.InferenceWithExecutor) {
	var ids []struct {
		InferenceId string
		ExecutorId  string
	}

	for _, inf := range inferences {
		ids = append(ids, struct {
			InferenceId string
			ExecutorId  string
		}{
			InferenceId: inf.Inference.InferenceId,
			ExecutorId:  inf.Executor.Index,
		})
	}

	slog.Debug("Validation: Inferences to sample", "ids", ids)
}

func logInferencesToValidate(toValidate []types.Inference) {
	var ids []string
	for _, inf := range toValidate {
		ids = append(ids, inf.InferenceId)
	}
	slog.Info("Validation: Inferences to validate", "inferences", ids)
}

func ShouldValidate(executor *types.Participant, currentAccountAddress string, currentAccountPower uint32, totalPower uint32) (bool, float32) {
	if executor.Index == currentAccountAddress {
		return false, 0.0
	}
	targetValidations := 1 - (executor.Reputation * 0.9)
	ourProbability := targetValidations * (float32(currentAccountPower) / float32(totalPower))
	slog.Info("Validation: ShouldValidate", "currentAccountPower", currentAccountPower, "totalPower", totalPower, "ourProbability", ourProbability)
	return rand.Float32() < ourProbability, ourProbability
}

func validateInferenceAndSendValMessage(inf types.Inference, nodeBroker *broker.Broker, transactionRecorder cosmosclient.InferenceCosmosClient, revalidation bool) {
	valResult, err := lockNodeAndValidate(inf, nodeBroker)
	if err != nil {
		slog.Error("Validation: Failed to validate inf.", "id", inf.InferenceId, "error", err)
		return
	}

	msgValidation, err := ToMsgValidation(valResult)
	if err != nil {
		slog.Error("Validation: Failed to convert to MsgValidation.", "id", inf.InferenceId, "error", err)
		return
	}
	msgValidation.Revalidation = revalidation

	if err = transactionRecorder.ReportValidation(msgValidation); err != nil {
		slog.Error("Validation: Failed to report validation.", "id", inf.InferenceId, "error", err)
		return
	}

	slog.Info("Validation: Successfully validated inference", "id", inf.InferenceId)
}

func ValidateByInferenceId(id string, node *broker.InferenceNode, transactionRecorder cosmosclient.InferenceCosmosClient) (ValidationResult, error) {
	queryClient := transactionRecorder.NewInferenceQueryClient()
	r, err := queryClient.Inference(context.Background(), &types.QueryGetInferenceRequest{Index: id})
	if err != nil {
		slog.Error("Validation: Failed get inference by id query", "id", id, "error", err)
	}

	return validate(r.Inference, node)
}

func lockNodeAndValidate(inference types.Inference, nodeBroker *broker.Broker) (ValidationResult, error) {
	return broker.LockNode(nodeBroker, testModel, func(node *broker.InferenceNode) (ValidationResult, error) {
		return validate(inference, node)
	})
}

func validate(inference types.Inference, inferenceNode *broker.InferenceNode) (ValidationResult, error) {
	slog.Debug("Validation: Validating inference", "id", inference.InferenceId)

	if inference.Status == types.InferenceStatus_STARTED {
		slog.Error("Validation: Inference not finished", "status", inference.Status, "inference", inference)
		return nil, errors.New("Inference is not finished. id = " + inference.InferenceId)
	}

	var requestMap map[string]interface{}
	if err := json.Unmarshal([]byte(inference.PromptPayload), &requestMap); err != nil {
		slog.Error("Validation: Failed to unmarshal inference.PromptPayload.", "id", inference.InferenceId, "error", err)
		return nil, err
	}

	originalResponse, err := unmarshalResponse(&inference)
	if err != nil {
		slog.Error("Validation: Failed to unmarshal inference.ResponsePayload.", "id", inference.InferenceId, "error", err)
		return nil, err
	}

	enforcedStr, err := originalResponse.GetEnforcedStr()
	if err != nil {
		return nil, err
	}
	requestMap["enforced_str"] = enforcedStr
	// A hack to simplify processing the response:
	requestMap["stream"] = false

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

	slog.Debug("responseValidation", "validation", string(respBodyBytes))
	var responseValidation completionapi.Response
	if err = json.Unmarshal(respBodyBytes, &responseValidation); err != nil {
		return nil, err
	}

	originalLogits := extractLogits(originalResponse)
	validationLogits := extractLogitsFromJsonResponse(responseValidation)
	baseResult := BaseValidationResult{
		InferenceId:   inference.InferenceId,
		ResponseBytes: respBodyBytes,
	}

	return compareLogits(originalLogits, validationLogits, baseResult), nil
}

type UnmarshalledResponse struct {
	JsonResponse     *completionapi.Response
	StreamedResponse *completionapi.StreamedResponse
}

func (r *UnmarshalledResponse) GetEnforcedStr() (string, error) {
	if r.JsonResponse != nil {
		return r.JsonResponse.Choices[0].Message.Content, nil
	} else if r.StreamedResponse != nil {
		var stringBuilder strings.Builder
		for _, event := range r.StreamedResponse.Data {
			if event.Choices[0].Delta.Content != nil {
				stringBuilder.WriteString(*event.Choices[0].Delta.Content)
			}
		}
		return stringBuilder.String(), nil
	} else {
		return "", errors.New("UnmarshalledResponse has invalid state, both responses are nil")
	}
}

func unmarshalResponse(inference *types.Inference) (*UnmarshalledResponse, error) {
	var genericMap map[string]interface{}
	if err := json.Unmarshal([]byte(inference.ResponsePayload), &genericMap); err != nil {
		log.Printf("Failed to unmarshal inference.ResponsePayload into generic map. id = %v. err = %v", inference.InferenceId, err)
		return nil, err
	}

	if _, exists := genericMap["events"]; exists {
		// It's likely a SerializedStreamedResponse
		events, err := unmarshalStreamedResponse(inference)
		if err != nil {
			return nil, err
		}
		return &UnmarshalledResponse{StreamedResponse: &completionapi.StreamedResponse{Data: events}}, nil
	} else {
		var originalResponse completionapi.Response
		if err := json.Unmarshal([]byte(inference.ResponsePayload), &originalResponse); err != nil {
			log.Printf("Failed to unmarshal inference.ResponsePayload into Response. id = %v. err = %v", inference.InferenceId, err)
			return nil, err
		}
		return &UnmarshalledResponse{JsonResponse: &originalResponse}, nil
	}
}

func unmarshalStreamedResponse(inference *types.Inference) ([]completionapi.Response, error) {
	var streamedResponse completionapi.SerializedStreamedResponse
	if err := json.Unmarshal([]byte(inference.ResponsePayload), &streamedResponse); err != nil {
		log.Printf("Failed to unmarshal inference.ResponsePayload into SerializedStreamedResponse. id = %v. err = %v", inference.InferenceId, err)
		return nil, err
	}
	log.Printf("Unmarshalled streamed response. inference.id = %s", inference.InferenceId)

	var unmarshalledEvents []completionapi.Response
	for _, line := range streamedResponse.Events {
		event, err := completionapi.UnmarshalEvent(line)
		if err != nil {
			return nil, err
		}
		if event != nil {
			unmarshalledEvents = append(unmarshalledEvents, *event)
		}
	}
	log.Printf("Unmarshalled events. inference.id = %s", inference.InferenceId)

	return unmarshalledEvents, nil
}

func extractLogits(response *UnmarshalledResponse) []completionapi.Logprob {
	if response.JsonResponse != nil {
		return extractLogitsFromJsonResponse(*response.JsonResponse)
	} else if response.StreamedResponse != nil {
		return extractLogitsFromStreamedResponse(*response.StreamedResponse)
	} else {
		return nil
	}
}

func extractLogitsFromJsonResponse(response completionapi.Response) []completionapi.Logprob {
	var logits []completionapi.Logprob
	// Concatenate all logrpobs
	for _, c := range response.Choices {
		logits = append(logits, c.Logprobs.Content...)
	}
	return logits
}

func extractLogitsFromStreamedResponse(response completionapi.StreamedResponse) []completionapi.Logprob {
	var logits []completionapi.Logprob
	for _, r := range response.Data {
		for _, c := range r.Choices {
			logits = append(logits, c.Logprobs.Content...)
		}
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
	originalLogits []completionapi.Logprob,
	validationLogits []completionapi.Logprob,
	baseComparisonResult BaseValidationResult,
) ValidationResult {
	if len(originalLogits) != len(validationLogits) {
		return &DifferentLengthValidationResult{baseComparisonResult}
	}

	var originalLogprobs, validationLogprobs []float64
	for i := range originalLogits {
		o := originalLogits[i]
		v := validationLogits[i]
		if o.Token != v.Token {
			return &DifferentTokensValidationResult{baseComparisonResult}
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
		cosineSimVal = result.(*CosineSimilarityValidationResult).Value
		log.Printf("Cosine similarity validation result. value = %v", cosineSimVal)
	default:
		slog.Error("Unknown validation result type", "type", fmt.Sprintf("%T", result), "result", result)
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
