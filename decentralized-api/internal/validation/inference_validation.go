package validation

import (
	"bytes"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/completionapi"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/utils"
	"decentralized-api/logging"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"

	"github.com/google/uuid"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

type InferenceValidator struct {
	recorder      cosmosclient.CosmosMessageClient
	nodeBroker    *broker.Broker
	configManager *apiconfig.ConfigManager
	phaseTracker  *chainphase.ChainPhaseTracker
}

func NewInferenceValidator(
	nodeBroker *broker.Broker,
	configManager *apiconfig.ConfigManager,
	recorder cosmosclient.CosmosMessageClient,
	phaseTracker *chainphase.ChainPhaseTracker) *InferenceValidator {
	return &InferenceValidator{
		nodeBroker:    nodeBroker,
		configManager: configManager,
		recorder:      recorder,
		phaseTracker:  phaseTracker,
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

	r, err := queryClient.Inference(recorder.GetContext(), &types.QueryGetInferenceRequest{Index: inferenceId})
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

	r, err := queryClient.GetInferenceValidationParameters(transactionRecorder.GetContext(), &types.QueryGetInferenceValidationParametersRequest{
		Ids:       ids,
		Requester: transactionRecorder.GetAddress(),
	})
	if err != nil {
		// FIXME: what should we do with validating the transaction?
		logging.Warn("Failed to query GetInferenceValidationParameters.", types.Validation, "error", err)
		return
	}

	params, err := queryClient.Params(transactionRecorder.GetContext(), &types.QueryParamsRequest{})
	if err != nil {
		logging.Error("Failed to get params", types.Validation, "error", err)
		return
	}

	logInferencesToSample(r.Details)

	address := transactionRecorder.GetAddress()
	var toValidateIds []string
	for _, inferenceWithExecutor := range r.Details {
		if inferenceWithExecutor.ExecutorId == address {
			continue
		}

		currentSeed := s.configManager.GetCurrentSeed().Seed
		if inferenceWithExecutor.TotalPower <= inferenceWithExecutor.ExecutorPower {
			logging.Warn("Total power is less than or equal to executor power, skipping validation", types.Validation, "inferenceId", inferenceWithExecutor.InferenceId, "totalPower", inferenceWithExecutor.TotalPower, "executorPower", inferenceWithExecutor.ExecutorPower)
			continue
		}
		shouldValidate, message := calculations.ShouldValidate(
			currentSeed,
			inferenceWithExecutor,
			uint32(inferenceWithExecutor.TotalPower),
			uint32(r.ValidatorPower),
			uint32(inferenceWithExecutor.ExecutorPower),
			params.Params.ValidationParams)
		logging.Info(message, types.Validation, "inferenceId", inferenceWithExecutor.InferenceId, "seed", currentSeed, "validator", transactionRecorder.GetAddress())
		if shouldValidate {
			toValidateIds = append(toValidateIds, inferenceWithExecutor.InferenceId)
		}
	}

	logInferencesToValidate(toValidateIds)
	for _, inf := range toValidateIds {
		go func() {
			response, err := queryClient.Inference(transactionRecorder.GetContext(), &types.QueryGetInferenceRequest{Index: inf})
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

	logging.Info("Inferences to sample", types.Validation, "ids", ids)
}

func logInferencesToValidate(toValidate []string) {
	var ids []string
	for _, inf := range toValidate {
		ids = append(ids, inf)
	}
	logging.Info("Inferences to validate", types.Validation, "inferences", ids)
}

func (s *InferenceValidator) validateInferenceAndSendValMessage(inf types.Inference, transactionRecorder cosmosclient.InferenceCosmosClient, revalidation bool) {
	valResult, err := broker.LockNode(s.nodeBroker, inf.Model, inf.NodeVersion, func(node *broker.Node) (ValidationResult, error) {
		return s.validate(inf, node)
	})

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

func (s *InferenceValidator) validate(inference types.Inference, inferenceNode *broker.Node) (ValidationResult, error) {
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

	enforcedTokens, err := originalResponse.GetEnforcedTokens()
	if err != nil {
		return nil, err
	}
	requestMap["enforced_tokens"] = enforcedTokens
	requestMap["stream"] = false
	delete(requestMap, "stream_options")

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
	responseValidation, err := completionapi.NewCompletionResponseFromBytes(respBodyBytes)
	if err != nil {
		logging.Error("Failed to unmarshal responseValidation", types.Validation, "id", inference.InferenceId, "error", err)
		return nil, err
	}

	originalLogits := originalResponse.ExtractLogits()
	validationLogits := responseValidation.ExtractLogits()
	baseResult := BaseValidationResult{
		InferenceId:   inference.InferenceId,
		ResponseBytes: respBodyBytes,
	}
	if len(originalLogits) == 0 || len(validationLogits) == 0 {
		logging.Error("No logits found in original or validation response", types.Validation, "id", inference.InferenceId, "originalLogits", originalLogits, "validationLogits", validationLogits)
		return nil, errors.New("no logits found in original or validation response")
	}

	return compareLogits(originalLogits, validationLogits, baseResult), nil
}

func unmarshalResponse(inference *types.Inference) (completionapi.CompletionResponse, error) {
	resp, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(inference.ResponsePayload)

	if err != nil {
		logging.Error("Failed to unmarshal inference.ResponsePayload.", types.Validation, "id", inference.InferenceId, "error", err)
	}

	switch resp.(type) {
	case *completionapi.StreamedCompletionResponse:
		logging.Info("Unmarshalled inference.ResponsePayload into StreamedResponse", types.Validation, "id", inference.InferenceId)
	case *completionapi.JsonCompletionResponse:
		logging.Info("Unmarshalled inference.ResponsePayload into JsonResponse", types.Validation, "id", inference.InferenceId)
	default:
		logging.Error("Failed to unmarshal inference.ResponsePayload into StreamedResponse or JsonResponse", types.Validation, "id", inference.InferenceId)
	}

	return resp, err
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
	totalLogprobs := max(100, len(originalLogprobs)) * len(originalLogprobs[0].TopLogprobs)

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
		logging.Warn("Different length validation result", types.Validation)
		simVal = 0
	case *DifferentTokensValidationResult:
		logging.Warn("Different tokens validation result", types.Validation)
		simVal = 0
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
