package validation

import (
	"math"
	"slices"

	"common/completionapi"
	"common/logging"

	"github.com/productscience/inference/x/inference/types"
)

// ScoringPolicy holds the constants of the short-output rule: the mean distance over every output
// position, never below the legacy sum/max(100, N), plus the per-position ceiling and
// sampler-support checks. See devshard/docs/proposals/short-output-validation.md.
type ScoringPolicy struct {
	// MeanAllowance is subtracted from the mean distance as MeanAllowance/sqrt(N): the honest
	// mean is noisier on a short output. It never takes the distance below the legacy
	// sum/max(100, N), so from N = 100 on the check is the legacy score.
	MeanAllowance float64
	// OutputPositionCeiling is the per-position ceiling check's distance bound.
	OutputPositionCeiling float64
	// OutputCeilingTolerance is the fraction of output positions allowed above the ceiling (floored).
	OutputCeilingTolerance float64
	// ProcessedCeilingFloor is the smallest ceiling budget with processed logprobs: a top-k entry
	// clamped to -9999 on one side alone puts an honest position above the ceiling.
	ProcessedCeilingFloor int
	// MaxOutOfSupportPositions is the sampler-support check's smallest budget.
	MaxOutOfSupportPositions int
	// OutOfSupportTolerance grows the sampler-support budget with N: an honest sampler emits
	// such tokens at a rate, not a fixed count.
	OutOfSupportTolerance float64
	// OutputChecksEnforcedModels lists the models the per-position ceiling and sampler-support
	// checks reject on; other models only log their failures (ForModel).
	OutputChecksEnforcedModels []string
	// OutputChecksLogOnly makes those two checks log instead of reject; ForModel sets it.
	OutputChecksLogOnly bool
}

// ForModel returns the policy with the output checks enforced only for OutputChecksEnforcedModels.
func (p ScoringPolicy) ForModel(model string) ScoringPolicy {
	p.OutputChecksLogOnly = !slices.Contains(p.OutputChecksEnforcedModels, model)
	return p
}

// DefaultShortOutputScoringPolicy is the short-output rule at its calibrated constants.
// See devshard/docs/proposals/short-output-validation.md.
var DefaultShortOutputScoringPolicy = ScoringPolicy{
	MeanAllowance:              0.10,
	OutputPositionCeiling:      0.30,
	OutputCeilingTolerance:     0.02,
	ProcessedCeilingFloor:      3,
	MaxOutOfSupportPositions:   1,
	OutOfSupportTolerance:      0.03,
	OutputChecksEnforcedModels: []string{"deepseek-ai/DeepSeek-V4-Flash-0731", "zai-org/GLM-5.3-Flash"},
}

// outOfSupportLogprob is at or below what vLLM serializes for a -inf logprob (it clamps to -9999).
const outOfSupportLogprob = -9999.0

// scorePositions computes the per-position distance; executor top_logprobs beyond the validated
// width are ignored so a padded width cannot perturb the fallback estimate.
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
	// outputCheck rejects, or under OutputChecksLogOnly only logs what it would have rejected.
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

	// Sampler-support check: with processed logprobs the validator scores an enforced token under
	// the genuine sampler, so -inf means that sampler could not have produced it.
	outOfSupportBudget := max(policy.MaxOutOfSupportPositions, int(math.Ceil(policy.OutOfSupportTolerance*float64(len(original)))))
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

	// Per-position ceiling check: never averaged, so a few fabricated positions cannot hide.
	allowed := int(policy.OutputCeilingTolerance * float64(len(original)))
	if processedLogprobs(logprobsMode) {
		allowed = max(allowed, policy.ProcessedCeilingFloor)
	}
	if countAbove(distances, policy.OutputPositionCeiling) > allowed {
		if rejected := outputCheck("per-position ceiling check", "Output positions diverge from the validator."); rejected != nil {
			return rejected
		}
	}

	// Mean-distance check: a true mean over every output position, compared with the chain threshold.
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

// stopTokenBeforeEnd reports whether a requested stop id was emitted before the last output
// position: the engine stops on it, so an output that continues past it was not generated as asked.
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
