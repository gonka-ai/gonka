package validation

import (
	"bytes"
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/completionapi"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/utils"
	"decentralized-api/logging"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

type InferenceValidator struct {
	recorder      cosmosclient.CosmosMessageClient
	nodeBroker    *broker.Broker
	configManager *apiconfig.ConfigManager
}

func NewInferenceValidator(
	nodeBroker *broker.Broker,
	configManager *apiconfig.ConfigManager,
	recorder cosmosclient.CosmosMessageClient) *InferenceValidator {
	return &InferenceValidator{
		nodeBroker:    nodeBroker,
		configManager: configManager,
		recorder:      recorder,
	}
}

func (s *InferenceValidator) VerifyInvalidation(events map[string][]string, recorder cosmosclient.InferenceCosmosClient) {
	inferenceIds, ok := events["inference_validation.inference_id"]
	if !ok || len(inferenceIds) == 0 {
		logging.Error("No inference_id found in events", types.Validation)
		return
	}
	inferenceId := inferenceIds[0]

	logging.Debug("Verifying invalidation", types.Validation, "inference_id", inferenceId)

	queryClient := recorder.NewInferenceQueryClient()

	r, err := queryClient.Inference(recorder.Context, &types.QueryGetInferenceRequest{Index: inferenceId})
	if err != nil {
		// FIXME: what should we do with validating the transaction?
		logging.Warn("Failed to query Inference for revalidation.", types.Validation, "error", err)
		return
	}

	logInferencesToValidate([]string{inferenceId})
	go func() {
		s.validateInferenceAndSendValMessage(r.Inference, recorder, true)
	}()

}

func (s *InferenceValidator) SampleInferenceToValidate(ids []string, transactionRecorder cosmosclient.InferenceCosmosClient) {
	if ids == nil {
		logging.Debug("No inferences to validate", types.Validation)
		return
	}

	logging.Debug("Sampling inf transactions to validate", types.Validation)

	queryClient := transactionRecorder.NewInferenceQueryClient()

	r, err := queryClient.GetInferenceValidationParameters(transactionRecorder.Context, &types.QueryGetInferenceValidationParametersRequest{
		Ids:       ids,
		Requester: transactionRecorder.Address,
	})
	if err != nil {
		// FIXME: what should we do with validating the transaction?
		logging.Warn("Failed to query GetInferenceValidationParameters.", types.Validation, "error", err)
		return
	}

	params, err := queryClient.Params(transactionRecorder.Context, &types.QueryParamsRequest{})
	if err != nil {
		logging.Error("Failed to get params", types.Validation, "error", err)
		return
	}

	logInferencesToSample(r.Details)

	var toValidateIds []string
	for _, inferenceWithExecutor := range r.Details {
		if inferenceWithExecutor.ExecutorId == transactionRecorder.Address {
			continue
		}

		currentSeed := s.configManager.GetCurrentSeed().Seed
		shouldValidate, message := calculations.ShouldValidate(
			currentSeed,
			inferenceWithExecutor,
			uint32(r.TotalPower),
			uint32(r.ValidatorPower),
			uint32(inferenceWithExecutor.ExecutorPower),
			params.Params.ValidationParams)
		logging.Debug("Should validate", types.Validation, "message", message, "inferenceId", inferenceWithExecutor.InferenceId, "seed", currentSeed)
		if shouldValidate {
			toValidateIds = append(toValidateIds, inferenceWithExecutor.InferenceId)
		}
	}

	logInferencesToValidate(toValidateIds)
	for _, inf := range toValidateIds {
		go func() {
			response, err := queryClient.Inference(transactionRecorder.Context, &types.QueryGetInferenceRequest{Index: inf})
			if err != nil {
				logging.Error("Failed to get inference by id", types.Validation, "id", response, "error", err)
				return
			}
			s.validateInferenceAndSendValMessage(response.Inference, transactionRecorder, false)
		}()
	}
}

func logInferencesToSample(inferences []*types.InferenceValidationDetails) {
	var ids []struct {
		InferenceId string
		ExecutorId  string
	}

	for _, inf := range inferences {
		ids = append(ids, struct {
			InferenceId string
			ExecutorId  string
		}{
			InferenceId: inf.InferenceId,
			ExecutorId:  inf.ExecutorId,
		})
	}

	logging.Debug("Inferences to sample", types.Validation, "ids", ids)
}

func logInferencesToValidate(toValidate []string) {
	var ids []string
	for _, inf := range toValidate {
		ids = append(ids, inf)
	}
	logging.Info("Inferences to validate", types.Validation, "inferences", ids)
}

func (s *InferenceValidator) validateInferenceAndSendValMessage(inf types.Inference, transactionRecorder cosmosclient.InferenceCosmosClient, revalidation bool) {
	valResult, err := s.lockNodeAndValidate(inf)
	if err != nil && errors.Is(err, broker.ErrNoNodesAvailable) {
		logging.Error("Failed to validate inference. No nodes available, probably unsupported model.", types.Validation, "id", inf.InferenceId, "error", err)
		valResult = ModelNotSupportedValidationResult{
			InferenceId: inf.InferenceId,
		}
	} else if err != nil {
		logging.Error("Failed to validate inference.", types.Validation, "id", inf.InferenceId, "error", err)
		return
	}

	msgValidation, err := ToMsgValidation(valResult)
	if err != nil {
		logging.Error("Failed to convert to MsgValidation.", types.Validation, "id", inf.InferenceId, "error", err)
		return
	}
	msgValidation.Revalidation = revalidation

	if err = transactionRecorder.ReportValidation(msgValidation); err != nil {
		logging.Error("Failed to report validation.", types.Validation, "id", inf.InferenceId, "error", err)
		return
	}

	logging.Info("Successfully validated inference", types.Validation, "id", inf.InferenceId)
}

func (s *InferenceValidator) ValidateByInferenceId(id string, node *broker.Node) (ValidationResult, error) {
	queryClient := s.recorder.NewInferenceQueryClient()
	r, err := queryClient.Inference(context.Background(), &types.QueryGetInferenceRequest{Index: id})
	if err != nil {
		logging.Error("Failed get inference by id query", types.Validation, "id", id, "error", err)
	}
	return s.Validate(r.Inference, node)
}

func (s *InferenceValidator) lockNodeAndValidate(inference types.Inference) (ValidationResult, error) {
	return broker.LockNode(s.nodeBroker, inference.Model, inference.NodeVersion, func(node *broker.Node) (ValidationResult, error) {
		return s.Validate(inference, node)
	})
}

func (s *InferenceValidator) Validate(inference types.Inference, inferenceNode *broker.Node) (ValidationResult, error) {
	logging.Debug("Validating inference", types.Validation, "id", inference.InferenceId)

	if inference.Status == types.InferenceStatus_STARTED {
		logging.Error("Inference not finished", types.Validation, "status", inference.Status, "inference", inference)
		return nil, errors.New("Inference is not finished. id = " + inference.InferenceId)
	}

	var requestMap map[string]interface{}
	if err := json.Unmarshal([]byte(inference.PromptPayload), &requestMap); err != nil {
		logging.Error("Failed to unmarshal inference.PromptPayload.", types.Validation, "id", inference.InferenceId, "error", err)
		return nil, err
	}

	originalResponse, err := unmarshalResponse(&inference)
	if err != nil {
		logging.Error("Failed to unmarshal inference.ResponsePayload.", types.Validation, "id", inference.InferenceId, "error", err)
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

	completionsUrl, err := url.JoinPath(inferenceNode.InferenceUrl(), "v1/chat/completions")
	if err != nil {
		logging.Error("Failed to join url", types.Validation, "url", inferenceNode.InferenceUrl(), "error", err)
		return nil, err
	}

	resp, err := http.Post(
		completionsUrl,
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

	logging.Debug("responseValidation", types.Validation, "validation", string(respBodyBytes))
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

type SimilarityValidationResult struct {
	BaseValidationResult
	Value float64
}

func (r SimilarityValidationResult) IsSuccessful() bool {
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

	for i := range originalLogits {
		o := originalLogits[i]
		v := validationLogits[i]
		if o.Token != v.Token {
			return &DifferentTokensValidationResult{baseComparisonResult}
		}
	}
	similarity := customSimilarity(originalLogits, validationLogits)

	return &SimilarityValidationResult{BaseValidationResult: baseComparisonResult, Value: similarity}
}

func customSimilarity(
	originalLogprobs []completionapi.Logprob,
	validationLogprobs []completionapi.Logprob,
) float64 {
	distance, err := customDistance(originalLogprobs, validationLogprobs)
	if err != nil {
		logging.Error("Error calculating custom distance", types.Validation, "error", err)
		return 0
	}
	similarity := 1 - distance
	if similarity < 0 {
		logging.Error("Similarity value is negative", types.Validation, "similarity", similarity)
		return 0
	}
	return similarity
}

func customDistance(
	originalLogprobs []completionapi.Logprob,
	validationLogprobs []completionapi.Logprob,
) (float64, error) {
	distance := 0.0
	for i := range originalLogprobs {
		o := originalLogprobs[i]
		v := validationLogprobs[i]
		posDistance, err := positionDistance(o.TopLogprobs, v.TopLogprobs)
		if err != nil {
			logging.Error("Error calculating position distance", types.Validation, "error", err)
			return math.Inf(1), err
		}
		distance += posDistance
	}
	totalLogprobs := len(originalLogprobs) * len(originalLogprobs[0].TopLogprobs)

	return distance / float64(totalLogprobs), nil
}

func positionDistance(
	originalLogprobs []completionapi.TopLogprobs,
	validationLogprobs []completionapi.TopLogprobs,
) (float64, error) {
	if len(originalLogprobs) == 0 || len(validationLogprobs) == 0 {
		return 0.0, fmt.Errorf("empty logprobs provided")
	}
	distance := 0.0

	originalLogprobMap := make(map[string]float64)
	for _, o := range originalLogprobs {
		originalLogprobMap[o.Token] = o.Logprob
	}
	sortedLogprobs := make([]float64, 0, len(originalLogprobMap))
	for _, logprob := range originalLogprobMap {
		sortedLogprobs = append(sortedLogprobs, logprob)
	}

	sort.Float64s(sortedLogprobs)

	var minOriginalLogprob1, minOriginalLogprob2 float64
	if len(sortedLogprobs) >= 2 {
		minOriginalLogprob1 = sortedLogprobs[0]
		minOriginalLogprob2 = sortedLogprobs[1]
	} else if len(sortedLogprobs) == 1 {
		minOriginalLogprob1 = sortedLogprobs[0]
		minOriginalLogprob2 = minOriginalLogprob1 - 100.0
	}

	// Estimate the next logprob value (2 as fine)
	nextOriginalLogprob := minOriginalLogprob1 - (minOriginalLogprob2 - minOriginalLogprob1)

	for _, v := range validationLogprobs {
		var originalLogprob float64
		if origProb, exists := originalLogprobMap[v.Token]; exists {
			originalLogprob = origProb
		} else {
			originalLogprob = nextOriginalLogprob
		}

		denom := 1e-6 + math.Abs(v.Logprob) + math.Abs(originalLogprob)
		distance += math.Abs(v.Logprob-originalLogprob) / denom / 2.0
	}

	return distance, nil
}

type ModelNotSupportedValidationResult struct {
	InferenceId string
}

func (r ModelNotSupportedValidationResult) GetInferenceId() string {
	return r.InferenceId
}

func (r ModelNotSupportedValidationResult) GetValidationResponseBytes() []byte {
	return nil
}

func (ModelNotSupportedValidationResult) IsSuccessful() bool {
	return false
}

func ToMsgValidation(result ValidationResult) (*inference.MsgValidation, error) {
	// Match type of result from implementations of ValidationResult
	var simVal float64
	switch result.(type) {
	case *DifferentLengthValidationResult:
		log.Printf("Different length validation result")
		simVal = -1
	case *DifferentTokensValidationResult:
		log.Printf("Different tokens validation result")
		simVal = -1
	case *SimilarityValidationResult:
		simVal = result.(*SimilarityValidationResult).Value
		logging.Info("Cosine similarity validation result", types.Validation, "cosineSimValue", simVal)
	case ModelNotSupportedValidationResult:
		simVal = 1
		logging.Info("Model not supported validation result. Assuming is valid", types.Validation, "inference_id", result.GetInferenceId())
	default:
		logging.Error("Unknown validation result type", types.Validation, "type", fmt.Sprintf("%T", result), "result", result)
		return nil, errors.New("unknown validation result type")
	}

	responseHash, _, err := utils.GetResponseHash(result.GetValidationResponseBytes())
	if err != nil {
		return nil, err
	}

	return &inference.MsgValidation{
		Id:              uuid.New().String(),
		InferenceId:     result.GetInferenceId(),
		ResponsePayload: string(result.GetValidationResponseBytes()),
		ResponseHash:    responseHash,
		Value:           simVal,
	}, nil
}
