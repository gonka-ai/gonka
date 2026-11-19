package inference

import (
	"errors"
	"fmt"

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

// judgeTape scores the answer on its tape from the replay the logprob
// comparison made, which costs the ML node nothing, and logs what it finds.
// In TapeEnforce a verdict that the comparison left valid turns invalid when
// the answer is off the tape. Nothing else it does reaches the vote: an answer
// it cannot judge keeps the comparison's.
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
	switch {
	case !tape.Pass():
		outcome, valid = "fail", false
	case !tape.EndPasses():
		outcome, valid = "end", false
	}
	details := []any{"steps", signals.Steps, "count", signals.Count, "max_gap", signals.MaxGap, "z", signals.Z}
	if signals.Masked > 0 {
		details = append(details, "masked", signals.Masked)
	}
	// What the request asked of the sampler, to read the signals against.
	for _, setting := range []struct {
		name  string
		value *float64
	}{{"temperature", tape.Temperature}, {"top_p", tape.TopP}, {"top_k", tape.TopK}} {
		if setting.value != nil {
			details = append(details, setting.name, *setting.value)
		}
	}
	if tape.Grammar {
		details = append(details, "grammar", true)
	}
	// How the answer ended: an answer cut short carries no end token, and the
	// replay runs one position past it.
	if tape.Finish != "" {
		details = append(details, "finish", tape.Finish)
	}
	if tape.ReplayPast > 0 {
		details = append(details, "replay_past", tape.ReplayPast)
	}
	if tape.End != "" {
		details = append(details, "end", tape.End, "end_gap", tape.EndGap)
	}
	fields = append(fields, "tape", outcome, "tape_valid", valid, "agree", valid == verdict.Valid)
	fields = append(fields, details...)
	if v.tapeMode == TapeEnforce && verdict.Valid && !valid {
		verdict.Valid = false
		verdict.Reason = "tape_" + outcome
		verdict.Details = details
	}
}
