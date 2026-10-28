package validation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// TapeReplayPath is the gonka_poc plugin route that replays committed tokens
// and returns TapeSignals with the completion.
const TapeReplayPath = "/api/v1/infval/chat/completions"

// TapeSignals summarise a tape replay: positions scored, positions whose
// committed token is not the validator's tape winner, and the largest and the
// summed score gap of those positions.
type TapeSignals struct {
	Steps  int     `json:"steps"`
	Count  int     `json:"count"`
	MaxGap float64 `json:"max_gap"`
	SumGap float64 `json:"sum_gap"`
}

// TapeThresholds challenge a response when any signal exceeds its cutoff.
type TapeThresholds struct {
	Count  int
	MaxGap float64
	SumGap float64
}

// DefaultTapeThresholds are the honest DeepSeek-V4-Flash FP8 maxima on B300 at
// 256 tokens, about 1% false challenges (calibration of 2026-10-28).
var DefaultTapeThresholds = TapeThresholds{Count: 32, MaxGap: 5.36, SumGap: 22.13}

func (t TapeThresholds) Pass(s TapeSignals) bool {
	return s.Count <= t.Count && s.MaxGap <= t.MaxGap && s.SumGap <= t.SumGap
}

// TapeValidationResult holds the signals of a tape replay.
type TapeValidationResult struct {
	BaseValidationResult
	Signals TapeSignals
}

func (r TapeValidationResult) IsSuccessful() bool {
	return DefaultTapeThresholds.Pass(r.Signals)
}

var errNoTapeSignals = errors.New("tape replay returned no gonka_tape signals")

// ExecuteTapeValidation is ExecuteValidation on the tape: the replay is flagged
// for it and execute must POST to TapeReplayPath.
func ExecuteTapeValidation(
	ctx context.Context,
	inferenceID string,
	promptPayload []byte,
	responsePayload []byte,
	execute func(ctx context.Context, body []byte) (*http.Response, error),
	claimedInputTokens, claimedOutputTokens uint64,
	logprobsMode string,
) (ValidationResult, error) {
	return executeValidation(ctx, inferenceID, promptPayload, responsePayload, execute,
		claimedInputTokens, claimedOutputTokens, logprobsMode, true)
}

func tapeResult(body []byte, committed int, base BaseValidationResult) (ValidationResult, error) {
	var replay struct {
		Tape *TapeSignals `json:"gonka_tape"`
	}
	if err := json.Unmarshal(body, &replay); err != nil {
		return nil, err
	}
	if replay.Tape == nil {
		return nil, errNoTapeSignals
	}
	if replay.Tape.Steps < committed {
		return &DifferentLengthValidationResult{base}, nil
	}
	return &TapeValidationResult{BaseValidationResult: base, Signals: *replay.Tape}, nil
}
