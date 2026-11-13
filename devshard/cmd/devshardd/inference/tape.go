package inference

import (
	"errors"
	"fmt"
	"time"

	commonvalidation "common/validation"
	devshardpkg "devshard"
	"devshard/logging"
)

// How the tape of an answer takes part in a validation (DEVSHARD_TAPE_VALIDATION).
const (
	// TapeShadow: the logprob comparison votes, the tape is logged beside it.
	TapeShadow = "shadow"
	// TapeEnforce: an answer off its tape is voted invalid as well.
	TapeEnforce = "enforce"
	// TapeOff: the tape is not scored.
	TapeOff = "off"
)

const (
	// A participant's positions off the winner are pooled over its recent
	// answers: the sum catches a substitute that passes answer by answer.
	poolWindow = 32
	poolMaxAge = 24 * time.Hour
	// The pool judges from pooledMinAnswers answers on, at a bound of 99.9 %.
	pooledQuantile   = 3.09
	pooledMinAnswers = 4
)

// judgeTape scores the answer on its tape from the replay the logprob
// comparison made, which costs the ML node nothing, and logs what it finds.
// In TapeEnforce a verdict that the comparison left valid turns invalid when
// the answer, or the participant's pool with it, is off the tape. Nothing else
// it does reaches the vote: an answer it cannot judge keeps the comparison's.
func (v *Validator) judgeTape(req devshardpkg.ValidateRequest, inferenceID string, promptPayload, responsePayload []byte,
	result commonvalidation.ValidationResult, verdict *devshardpkg.ValidateResult) {
	if v.tapeMode != TapeShadow && v.tapeMode != TapeEnforce {
		return
	}
	fields := []any{"subsystem", "validation", "inferenceId", inferenceID, "executor", req.ExecutorAddress,
		"model", req.Model, "mode", v.tapeMode, "voted", verdict.Valid}
	outcome, valid := "pass", true
	defer func() {
		if r := recover(); r != nil {
			fields = append(fields, "tape", "error", "cause", fmt.Sprintf("panic: %v", r))
		}
		logging.Info("tape validation", fields...)
	}()

	tape, err := commonvalidation.TapeOfReplay(inferenceID, promptPayload, responsePayload,
		result.GetValidationResponseBytes(), v.chainParams.LogprobsMode())
	if err != nil {
		outcome = "error"
		if errors.Is(err, commonvalidation.ErrTapeUnjudged) {
			outcome = "unjudged"
		}
		fields = append(fields, "tape", outcome, "cause", err.Error())
		return
	}
	signals := tape.Signals
	if !tape.Pass() {
		outcome, valid = "fail", false
	}
	details := []any{"steps", signals.Steps, "count", signals.Count, "max_gap", signals.MaxGap, "z", signals.Z}
	if signals.Masked > 0 {
		details = append(details, "masked", signals.Masked)
	}
	if v.pool != nil {
		answers, steps, count := v.pool.Observe(req.ExecutorAddress, req.Model, signals.Steps, signals.Count)
		details = append(details, "pooled_answers", answers, "pooled_steps", steps, "pooled_count", count)
		thresholds, _ := commonvalidation.TapeThresholdsFor(req.Model)
		if valid && answers >= pooledMinAnswers && !thresholds.CountPasses(steps, count, pooledQuantile) {
			outcome, valid = "pooled_flips", false
		}
	}
	fields = append(fields, "tape", outcome, "tape_valid", valid, "agree", valid == verdict.Valid)
	fields = append(fields, details...)
	if v.tapeMode == TapeEnforce && verdict.Valid && !valid {
		verdict.Valid = false
		verdict.Reason = "tape_" + outcome
		verdict.Details = details
	}
}
