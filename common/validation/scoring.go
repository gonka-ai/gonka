package validation

import (
	"math"
	"slices"

	"common/completionapi"
	"common/logging"

	"github.com/productscience/inference/x/inference/types"
)

// ScoringPolicy holds the constants of the short-output scoring rule.
// See devshard/docs/proposals/short-output-validation.md.
type ScoringPolicy struct {
	MeanAllowance              float64
	OutputPositionCeiling      float64
	OutputCeilingTolerance     float64
	ProcessedCeilingFloor      int
	MaxOutOfSupportPositions   int
	OutOfSupportTolerance      float64
	NarrowSamplerTolerance     float64
	NarrowSampler              bool // set by ForRequest
	OutputChecksEnforcedModels []string
	OutputChecksLogOnly        bool // set by ForModel
}

// ForModel returns the policy with the output checks enforced only for OutputChecksEnforcedModels.
func (p ScoringPolicy) ForModel(model string) ScoringPolicy {
	p.OutputChecksLogOnly = !slices.Contains(p.OutputChecksEnforcedModels, model)
	return p
}

const (
	calibratedTopK        = 40
	calibratedTopP        = 0.95
	calibratedTemperature = 0.7
)

// ForRequest returns the policy with NarrowSampler set when the request samples narrower than the
// calibration corpora, counting a top_p mask under a temperature below theirs as narrower.
func (p ScoringPolicy) ForRequest(request map[string]interface{}) ScoringPolicy {
	topK, _ := request["top_k"].(float64)
	topP, hasTopP := request["top_p"].(float64)
	minP, _ := request["min_p"].(float64)
	temperature, hasTemperature := request["temperature"].(float64)
	p.NarrowSampler = (topK >= 1 && topK < calibratedTopK) || (hasTopP && topP < calibratedTopP) || minP > 0 ||
		(hasTopP && topP < 1 && hasTemperature && temperature < calibratedTemperature)
	return p
}

// DefaultShortOutputScoringPolicy is the short-output rule at its calibrated constants.
var DefaultShortOutputScoringPolicy = ScoringPolicy{
	MeanAllowance:              0.10,
	OutputPositionCeiling:      0.30,
	OutputCeilingTolerance:     0.02,
	ProcessedCeilingFloor:      3,
	MaxOutOfSupportPositions:   1,
	OutOfSupportTolerance:      0.03,
	NarrowSamplerTolerance:     0.50,
	OutputChecksEnforcedModels: []string{"deepseek-ai/DeepSeek-V4-Flash-0731", "zai-org/GLM-5.3-Flash"},
}

// outOfSupportLogprob is what vLLM serializes for a -inf logprob.
const outOfSupportLogprob = -9999.0

// scorePositions ignores executor top_logprobs beyond the validated width, so padding cannot shift the score.
func scorePositions(original, validation []completionapi.Logprob) ([]float64, error) {
	distances := make([]float64, len(original))
	for i := range original {
		originalTop := original[i].TopLogprobs
		validationTop := validation[i].TopLogprobs
		if len(originalTop) > len(validationTop) {
			originalTop = originalTop[:len(validationTop)]
		}
		distance, err := positionDistance(originalTop, validationTop)
		if err != nil {
			return nil, err
		}
		distances[i] = distance
	}
	return distances, nil
}

// shortOutputVerdict runs the sampler-support, per-position ceiling and mean-distance checks.
func shortOutputVerdict(
	original, validation []completionapi.Logprob,
	policy ScoringPolicy,
	logprobsMode string,
	base BaseValidationResult,
) ValidationResult {
	outputCheck := func(check, reason string) ValidationResult {
		if policy.OutputChecksLogOnly {
			logging.Warn(check+" would fail (log-only): "+reason, types.Validation, "inferenceId", base.InferenceId)
			return nil
		}
		logging.Warn(check+" failed: "+reason, types.Validation, "inferenceId", base.InferenceId)
		return &InvalidInferenceResult{InferenceId: base.InferenceId, Reason: reason}
	}

	if len(original) == 0 {
		return &SimilarityValidationResult{BaseValidationResult: base, Value: 1}
	}

	// Processed logprobs score the token under the genuine sampler, so -inf means it could not have produced it.
	tolerance := policy.OutOfSupportTolerance
	if policy.NarrowSampler {
		tolerance = policy.NarrowSamplerTolerance
	}
	outOfSupportBudget := max(policy.MaxOutOfSupportPositions, int(math.Ceil(tolerance*float64(len(original)))))
	if processedLogprobs(logprobsMode) && outOfSupportPositions(validation[:len(original)]) > outOfSupportBudget {
		if rejected := outputCheck("sampler-support check", "Output tokens outside the sampler support."); rejected != nil {
			return rejected
		}
	}

	distances, err := scorePositions(original, validation)
	if err != nil {
		logging.Error("Error calculating position distance", types.Validation, "error", err)
		return &SimilarityValidationResult{BaseValidationResult: base, Value: 0}
	}

	allowed := int(policy.OutputCeilingTolerance * float64(len(original)))
	if processedLogprobs(logprobsMode) {
		allowed = max(allowed, policy.ProcessedCeilingFloor)
	}
	if countAbove(distances, policy.OutputPositionCeiling) > allowed {
		if rejected := outputCheck("per-position ceiling check", "Output positions diverge from the validator."); rejected != nil {
			return rejected
		}
	}

	// Never below the legacy sum/max(100, N), so from N = 100 on this is the legacy score.
	sum := 0.0
	for _, distance := range distances {
		sum += distance
	}
	n := float64(len(distances))
	mean := max(sum/n-policy.MeanAllowance/math.Sqrt(n), sum/max(n, 100))
	return &SimilarityValidationResult{BaseValidationResult: base, Value: similarityFromDistance(mean)}
}

func similarityFromDistance(distance float64) float64 {
	if math.IsNaN(distance) || math.IsInf(distance, 0) || distance > 1 {
		return 0
	}
	return 1 - distance
}

func countAbove(distances []float64, ceiling float64) int {
	count := 0
	for _, distance := range distances {
		if distance > ceiling {
			count++
		}
	}
	return count
}

func outOfSupportPositions(validation []completionapi.Logprob) int {
	count := 0
	for _, position := range validation {
		if math.IsNaN(position.Logprob) || position.Logprob <= outOfSupportLogprob {
			count++
		}
	}
	return count
}

func processedLogprobs(logprobsMode string) bool {
	return logprobsMode == "processed" || logprobsMode == "processed_logprobs"
}

// stopTokenBeforeEnd reports whether a requested stop id appears before the last output position.
func stopTokenBeforeEnd(output []completionapi.Logprob, stopTokenIDs map[string]struct{}) bool {
	if len(stopTokenIDs) == 0 {
		return false
	}
	for _, position := range output[:max(len(output)-1, 0)] {
		if _, isStop := stopTokenIDs[position.Token]; isStop {
			return true
		}
	}
	return false
}
