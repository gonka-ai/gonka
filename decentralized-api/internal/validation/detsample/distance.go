package detsample

// Stage-2 distance ("Check 1"). The validator recomputes logprobs on the model
// and compares them to the signed ones; a large distance means the signed
// distribution is not this model's. This mirrors validation_sampling.mae_distance
// (Python) so the chain and vLLM agree on the metric.
//
// The metric is the mean over positions of the mean absolute logprob difference
// over the executor's reported top-K support. GPU-evidence follow-up Experiment 4
// (gonka-ai/gonka#1199): MAE over the top-K support separates honest from a wrong
// model by ~40-65x, while the sampled-token delta alone overlaps and cannot gate.

import (
	"math"
	"sort"
)

// Stage2MAEFraudThreshold is the fraud cut on MAEDistance. PLACEHOLDER pending
// Decision D (#1199): Experiments 4/5 put the honest floor near ~0.01 MAE and a
// clearly different model near ~0.4-0.8, with int8 quantization a gray zone around
// ~0.1-0.2. 0.25 accepts int8 as "the model" while still catching int4 and cheaper
// models. The final value — and whether int8 counts as the model — is a policy
// decision, NOT a measurement. Not enforcing.
const Stage2MAEFraudThreshold = 0.25

// maeMissingPenalty is charged for a token present on the executor side but absent
// on the validator side, so a truncated/mismatched distribution reads as distant.
const maeMissingPenalty = 10.0

// MAEDistance returns the mean-over-positions MAE over the executor's reported
// support. Returns 10.0 (maximally distant) on a length mismatch, 0.0 on empty.
func MAEDistance(executor, validator []map[string]float64) float64 {
	if len(executor) == 0 {
		return 0.0
	}
	if len(executor) != len(validator) {
		return 10.0 // length mismatch -> maximally distant
	}

	perPosition := make([]float64, 0, len(executor))
	for i, exec := range executor {
		if len(exec) == 0 {
			continue
		}
		val := validator[i]
		// Sum over sorted token IDs so the float accumulation order matches the
		// Python validator's (byte-exact cross-language distance).
		tids := make([]string, 0, len(exec))
		for tid := range exec {
			tids = append(tids, tid)
		}
		sort.Strings(tids)
		var sum float64
		for _, tid := range tids {
			if v, ok := val[tid]; ok {
				sum += math.Abs(exec[tid] - v)
			} else {
				sum += maeMissingPenalty
			}
		}
		perPosition = append(perPosition, sum/float64(len(exec)))
	}

	if len(perPosition) == 0 {
		return 0.0
	}
	var total float64
	for _, d := range perPosition {
		total += d
	}
	return total / float64(len(perPosition))
}
