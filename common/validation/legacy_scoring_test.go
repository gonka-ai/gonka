package validation

import (
	"math"

	"common/completionapi"
	"common/logging"

	"github.com/productscience/inference/x/inference/types"
)

// CompareLogits is the legacy max(100, N) scorer, the baseline the short-output tests compare
// against.
func CompareLogits(
	originalLogits []completionapi.Logprob,
	validationLogits []completionapi.Logprob,
	baseComparisonResult BaseValidationResult,
) ValidationResult {
	if mismatch := compareTokens(originalLogits, validationLogits, baseComparisonResult); mismatch != nil {
		return mismatch
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
