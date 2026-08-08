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
		// Ignore executor top_logprobs beyond the validated width so a padded width can neither dilute the divisor nor perturb the fallback (H1 #3853145).
		originalTopLogprobs := o.TopLogprobs
		if len(originalTopLogprobs) > len(v.TopLogprobs) {
			originalTopLogprobs = originalTopLogprobs[:len(v.TopLogprobs)]
		}
		posDistance, err := positionDistance(originalTopLogprobs, v.TopLogprobs)
		if err != nil {
			logging.Error("Error calculating position distance", types.Validation, "error", err)
			return math.Inf(1), err
		}
		distance += posDistance
	}
	totalLogprobs := max(100, len(originalLogprobs))

	return distance / float64(totalLogprobs), nil
}

// maxPositionTerm is the supremum of a single token's contribution: |a-b|/(1e-6+|a|+|b|)/2 stays
// below 0.5 for finite a,b, so a non-finite (untrusted) executor logprob is scored at this maximum.
const maxPositionTerm = 0.5

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
		originalLogprob, matched := originalLogprobMap[v.Token]
		if !matched {
			originalLogprob = nextOriginalLogprob
		}

		if math.IsInf(originalLogprob, 0) || math.IsNaN(originalLogprob) ||
			math.IsInf(v.Logprob, 0) || math.IsNaN(v.Logprob) {
			distance += maxPositionTerm
			continue
		}

		denom := 1e-6 + math.Abs(v.Logprob) + math.Abs(originalLogprob)
		distance += math.Abs(v.Logprob-originalLogprob) / denom / 2.0
	}

	return distance / float64(len(validationLogprobs)), nil
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

	// A 4xx (400/422) from the validator's own re-execution means the validator
	// could not process the executor-supplied request (e.g. the original inference
	// failed on upstream payload rejection, or a validator on an older version
	// cannot re-execute it). Mainnet treats this as autopass (warn + pass), not
	// invalid: absent proof the executor cheated, it is not punished. Keep mainnet
	// semantics and rely on the warn logs to surface any cases worth marking
	// invalid later. Ref: decentralized-api/internal/validation/inference_validation.go (~944).
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity {
		logging.Warn("validator re-execution rejected payload; treating validation as passed (mainnet 4xx autopass)",
			types.Validation, "inferenceId", inferenceID, "status", resp.StatusCode)
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
	// CompareLogits short-circuits to perfect similarity (1.0) when the ORIGINAL
	// logits are empty, so an executor that stored a response with no logprobs
	// would always pass. Reject only the asymmetric case (exactly one side empty):
	// the executor's output cannot be verified against the validator's
	// re-execution.
	//
	// Both-empty intentionally autopasses (unlike mainnet's || fail-closed guard):
	// legitimate reasoning-burn empties (e.g. Kimi-K2.6, finish_reason=length)
	// still match. Keep warn+pass for now and gather logs before deciding whether
	// to fail-close or require an explicit "legitimate empty" shape.
	if (len(originalLogits) == 0) != (len(validationLogits) == 0) {
		logging.Warn("validation failed: logit presence mismatch between original and validation response",
			types.Validation,
			"inferenceId", inferenceID,
			"originalLogits", len(originalLogits),
			"validationLogits", len(validationLogits),
		)
		return &InvalidInferenceResult{
			InferenceId: inferenceID,
			Reason:      "Logit presence mismatch between original and validation response.",
		}, nil
	}
	if len(originalLogits) == 0 {
		logging.Warn("both original and validation logits empty; treating validation as passed (both-empty autopass)",
			types.Validation,
			"inferenceId", inferenceID,
		)
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
