package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"

	"common/completionapi"
	"common/logging"
	"common/utils"

	"github.com/google/uuid"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

// ErrPayloadUnavailable indicates payloads could not be retrieved after all retries
// and the inference is post-upgrade (no on-chain fallback available).
var ErrPayloadUnavailable = errors.New("payload unavailable after all retries")

// ValidationResult is the interface for all validation outcomes.
type ValidationResult interface {
	GetInferenceId() string

	GetValidationResponseBytes() []byte

	IsSuccessful() bool
}

// BaseValidationResult holds common fields for validation results.
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

// DifferentLengthValidationResult is returned when logit lengths differ.
type DifferentLengthValidationResult struct {
	BaseValidationResult
}

func (DifferentLengthValidationResult) IsSuccessful() bool {
	return false
}

// DifferentTokensValidationResult is returned when tokens differ.
type DifferentTokensValidationResult struct {
	BaseValidationResult
}

func (DifferentTokensValidationResult) IsSuccessful() bool {
	return false
}

// SimilarityValidationResult holds a cosine similarity value.
type SimilarityValidationResult struct {
	BaseValidationResult
	Value float64
}

// LegacySimilarityThreshold is the historical default pass bar used when no
// per-model threshold is available. Prefer SimilarityPassesThreshold with an
// explicit model threshold from chain/runtime config.
const LegacySimilarityThreshold = 0.99

// SimilarityPassesThreshold reports whether similarity clears the pass bar.
func SimilarityPassesThreshold(similarity, threshold float64) bool {
	return similarity > threshold
}

// DecimalToFloat converts a cosmos LegacyDec encoded as value * 10^exponent.
func DecimalToFloat(value int64, exponent int32) float64 {
	return float64(value) * math.Pow(10, float64(exponent))
}

func (r SimilarityValidationResult) IsSuccessful() bool {
	return SimilarityPassesThreshold(r.Value, LegacySimilarityThreshold)
}

// InvalidInferenceResult represents a validation failure with a reason.
type InvalidInferenceResult struct {
	InferenceId string
	Reason      string
	Error       error
}

func (r InvalidInferenceResult) IsSuccessful() bool {
	return false
}

func (r InvalidInferenceResult) GetInferenceId() string {
	return r.InferenceId
}

func (r InvalidInferenceResult) GetValidationResponseBytes() []byte {
	return []byte{}
}

const emptySentinelToken = "<EMPTY>"

// IsEmptySentinelTokens reports whether the enforced tokens contain only the empty sentinel.
func IsEmptySentinelTokens(et completionapi.EnforcedTokens) bool {
	for _, t := range et.Tokens {
		if t.Token == emptySentinelToken {
			return true
		}
	}
	return false
}

// HasNonNumericTokens reports whether any token ID in the enforced tokens is non-numeric.
func HasNonNumericTokens(et completionapi.EnforcedTokens) bool {
	for _, t := range et.Tokens {
		n, err := strconv.Atoi(t.Token)
		if err != nil || n < 0 {
			return true
		}
		for _, topToken := range t.TopTokens {
			n, err := strconv.Atoi(topToken)
			if err != nil || n < 0 {
				return true
			}
		}
	}
	return false
}

func validationReplaySeed(inferenceID string) int32 {
	parsed, err := strconv.ParseUint(inferenceID, 10, 64)
	if err != nil {
		return 0
	}
	if parsed > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(parsed)
}

// TokenCountInflated reports whether claimed token usage exceeds validation replay
// by more than the v2 tolerance (3 tokens).
func TokenCountInflated(claimed, validation uint64) bool {
	const tokenCountTolerance uint64 = 3
	return claimed > validation && claimed-validation > tokenCountTolerance
}

// CompareLogits compares original and validation logits and returns a ValidationResult.
func CompareLogits(
	originalLogits []completionapi.Logprob,
	validationLogits []completionapi.Logprob,
	baseComparisonResult BaseValidationResult,
) ValidationResult {
	if len(originalLogits) != len(validationLogits) {
		logging.Warn("Different length of logits", types.Validation, "inferenceId", baseComparisonResult.InferenceId, "originalLogits", originalLogits, "validationLogits", validationLogits, "lengthOriginal", len(originalLogits), "lengthValidation", len(validationLogits))
	}
	if len(validationLogits) < len(originalLogits) {
		logging.Warn("Validation logits are shorter than original logits", types.Validation, "inferenceId", baseComparisonResult.InferenceId, "originalLogits", originalLogits, "validationLogits", validationLogits, "lengthOriginal", len(originalLogits), "lengthValidation", len(validationLogits))
		return &DifferentLengthValidationResult{baseComparisonResult}
	}

	for i := range originalLogits {
		o := originalLogits[i]
		v := validationLogits[i]
		if o.Token != v.Token {
			logging.Error("Different tokens in logits", types.Validation, "inferenceId", baseComparisonResult.InferenceId, "originalLogits", originalLogits, "validationLogits", validationLogits)
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
	if math.IsNaN(distance) || math.IsInf(distance, 0) {
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
	if len(originalLogprobs) == 0 {
		return 0.0, nil
	}
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
	totalLogprobs := max(100, len(originalLogprobs))
	if len(originalLogprobs[0].TopLogprobs) > 0 {
		totalLogprobs *= len(originalLogprobs[0].TopLogprobs)
	}

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
		if math.IsNaN(denom) || denom == 0 {
			continue
		}
		term := math.Abs(v.Logprob-originalLogprob) / denom / 2.0
		if !math.IsNaN(term) {
			distance += term
		}
	}

	return distance, nil
}

// getResponseHash hashes the response bytes after unmarshalling.
// Inlined from decentralized-api/internal/utils.GetResponseHash.
func getResponseHash(bodyBytes []byte) (string, error) {
	if len(bodyBytes) == 0 {
		return "", nil
	}
	var response completionapi.Response
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return "", err
	}
	// Hash full bytes to include logprobs, preventing manipulation attacks
	hash := utils.GenerateSHA256Hash(string(bodyBytes))
	return hash, nil
}

// ToMsgValidation converts a ValidationResult to a chain message.
func ToMsgValidation(result ValidationResult) (*inference.MsgValidation, error) {
	// Match type of result from implementations of ValidationResult
	var simVal float64
	switch r := result.(type) {
	case *DifferentLengthValidationResult:
		logging.Warn("Different length validation result", types.Validation)
		simVal = 0
	case *DifferentTokensValidationResult:
		logging.Warn("Different tokens validation result", types.Validation)
		simVal = 0
	case *SimilarityValidationResult:
		simVal = r.Value
		logging.Info("Cosine similarity validation result", types.Validation, "cosineSimValue", simVal)
	case *InvalidInferenceResult:
		simVal = 0
		logging.Warn("Invalid inference result", types.Validation, "reason", r.Reason, "inferenceId", r.GetInferenceId(), "error", r.Error)
	default:
		logging.Error("Unknown validation result type", types.Validation, "type", fmt.Sprintf("%T", result), "result", result)
		return nil, errors.New("unknown validation result type")
	}

	responseHash, err := getResponseHash(result.GetValidationResponseBytes())
	if err != nil {
		logging.Error("Failed to get response hash", types.Validation, "error", err)
		return nil, err
	}

	return &inference.MsgValidation{
		Id:           uuid.New().String(),
		InferenceId:  result.GetInferenceId(),
		ResponseHash: responseHash,
		// The conversion may not be deterministic here, but that doesn't matter as the message
		// itself is what counts, and it WILL be deterministic
		ValueDecimal: DecimalFromFloat(simVal),
	}, nil
}

var zero = inference.Decimal{Value: 0, Exponent: 0}

// DecimalFromFloat converts a float64 to an inference.Decimal.
func DecimalFromFloat(f float64) *inference.Decimal {
	d := decimal.NewFromFloat(f)
	return &inference.Decimal{Value: d.CoefficientInt64(), Exponent: d.Exponent()}
}

// ExecuteValidation builds and executes a validation request from stored payloads,
// then compares logits. execute receives the constructed JSON body and should POST
// it to the validator's ML node; the response is compared against the original.
// claimedInputTokens and claimedOutputTokens are what the executor reported; if
// the validator's re-execution uses fewer tokens, validation fails to catch inflation.
// Pass 0 for both to skip the token count check.
func ExecuteValidation(
	ctx context.Context,
	inferenceID string,
	promptPayload []byte,
	responsePayload []byte,
	execute func(ctx context.Context, body []byte) (*http.Response, error),
	claimedInputTokens, claimedOutputTokens uint64,
	logprobsMode string,
) (ValidationResult, error) {
	var requestMap map[string]interface{}
	modifiedRequest, err := completionapi.ModifyRequestBodyWithLogprobsMode(
		promptPayload,
		validationReplaySeed(inferenceID),
		logprobsMode,
	)
	if err != nil {
		return &InvalidInferenceResult{inferenceID, "Failed to modify promptPayload.", err}, nil
	}
	if err := json.Unmarshal(modifiedRequest.NewBody, &requestMap); err != nil {
		return &InvalidInferenceResult{inferenceID, "Failed to unmarshal promptPayload.", err}, nil
	}

	originalResponse, err := UnmarshalResponsePayload(responsePayload)
	if err != nil {
		return &InvalidInferenceResult{inferenceID, "Failed to unmarshal responsePayload.", err}, nil
	}

	enforcedTokens, err := originalResponse.GetEnforcedTokens()
	if err != nil {
		return &InvalidInferenceResult{inferenceID, "Failed to get enforced tokens.", err}, nil
	}

	isEmptySentinel := IsEmptySentinelTokens(enforcedTokens)

	if !isEmptySentinel && HasNonNumericTokens(enforcedTokens) {
		logging.Warn("Executor response contains non-numeric token strings in logprobs instead of token IDs", types.Validation,
			"inferenceId", inferenceID)
		return &InvalidInferenceResult{inferenceID, "Logprobs contain decoded text instead of numeric token IDs.", nil}, nil
	}

	if isEmptySentinel {
		logging.Info("Detected empty sentinel response; replaying prompt without enforced tokens to verify executor failure", types.Validation,
			"inferenceId", inferenceID)
		delete(requestMap, "enforced_tokens")
	} else {
		requestMap["enforced_tokens"] = enforcedTokens
	}
	requestMap["stream"] = false
	requestMap["skip_special_tokens"] = false
	delete(requestMap, "stream_options")

	requestBody, err := json.Marshal(requestMap)
	if err != nil {
		return nil, err
	}

	resp, err := execute(ctx, requestBody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// If the validator's inference node rejects the payload (400/422), treat as passed.
	// This can happen when the original inference could not be executed due to upstream
	// payload rejection, and validators on older versions may still attempt re-execution.
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
		logging.Warn("Validator inference node rejected payload; treating validation as passed", types.Validation,
			"inferenceId", inferenceID, "status", resp.StatusCode)
		return &SimilarityValidationResult{
			BaseValidationResult: BaseValidationResult{InferenceId: inferenceID, ResponseBytes: []byte{}},
			Value:                1.0,
		}, nil
	}

	if isEmptySentinel && resp.StatusCode == http.StatusOK {
		logging.Warn("Executor returned error but validator successfully served the prompt", types.Validation,
			"inferenceId", inferenceID)
		return &InvalidInferenceResult{inferenceID, "Executor returned error but prompt is servable.", nil}, nil
	}

	logging.Debug("responseValidation", types.Validation, "validation", string(respBodyBytes))
	responseValidation, err := completionapi.NewCompletionResponseFromBytes(respBodyBytes)
	if err != nil {
		logging.Error("Failed to unmarshal responseValidation", types.Validation, "id", inferenceID, "error", err)
		return nil, err
	}

	if validationUsage, err := responseValidation.GetUsage(); err == nil {
		if TokenCountInflated(claimedInputTokens, validationUsage.PromptTokens) ||
			TokenCountInflated(claimedOutputTokens, validationUsage.CompletionTokens) {
			logging.Warn("validation failed: inflated token counts", types.Validation,
				"inferenceId", inferenceID,
				"claimedInput", claimedInputTokens, "validationInput", validationUsage.PromptTokens,
				"claimedOutput", claimedOutputTokens, "validationOutput", validationUsage.CompletionTokens)
			return &InvalidInferenceResult{InferenceId: inferenceID, Reason: "Inflated token counts."}, nil
		}
	}

	originalLogits := originalResponse.ExtractLogits()
	validationLogits := responseValidation.ExtractLogits()
	baseResult := BaseValidationResult{InferenceId: inferenceID, ResponseBytes: respBodyBytes}
	if len(originalLogits) == 0 || len(validationLogits) == 0 {
		logging.Error("No logits found in original or validation response",
			types.Validation,
			"id", inferenceID,
			"originalLogits", originalLogits,
			"validationLogits", validationLogits,
		)
		return nil, errors.New("no logits found in original or validation response")
	}

	return CompareLogits(originalLogits, validationLogits, baseResult), nil
}

func UnmarshalResponsePayload(responsePayload []byte) (completionapi.CompletionResponse, error) {
	resp, err := completionapi.NewCompletionResponseFromLinesFromResponsePayload(responsePayload)
	if err != nil {
		logging.Error("Failed to unmarshal responsePayload", types.Validation, "error", err)
	}
	switch resp.(type) {
	case *completionapi.StreamedCompletionResponse:
		logging.Debug("Unmarshalled responsePayload into StreamedResponse", types.Validation)
	case *completionapi.JsonCompletionResponse:
		logging.Debug("Unmarshalled responsePayload into JsonResponse", types.Validation)
	default:
		logging.Error("Failed to unmarshal responsePayload into StreamedResponse or JsonResponse", types.Validation)
	}
	return resp, err
}
