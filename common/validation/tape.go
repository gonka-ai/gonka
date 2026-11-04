package validation

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
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

// TapeThresholds challenge a response when any signal exceeds its cutoff. An
// honest response flips a steady share of its positions, so the Count and
// SumGap cutoffs grow with the positions scored past a fixed floor.
type TapeThresholds struct {
	Count      float64
	CountRate  float64
	MaxGap     float64
	SumGap     float64
	SumGapRate float64
}

// DefaultTapeThresholds are the honest DeepSeek-V4-Flash FP8 maxima on B300 at
// 256 tokens, about 1% false challenges (calibration of 2026-10-28), carried
// past 256 tokens at the same rate per position.
var DefaultTapeThresholds = TapeThresholds{
	Count: 32, CountRate: 32.0 / 256,
	MaxGap: 5.36,
	SumGap: 22.13, SumGapRate: 22.13 / 256,
}

func (t TapeThresholds) Pass(s TapeSignals) bool {
	n := float64(s.Steps)
	return float64(s.Count) <= max(t.Count, t.CountRate*n) &&
		s.MaxGap <= t.MaxGap &&
		s.SumGap <= max(t.SumGap, t.SumGapRate*n)
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

// servesWithoutCommitted reports whether the validator serves a rejected replay
// once the committed tokens are out of it: the rejection is then of the
// executor's own output, not of a request this validator cannot run.
func servesWithoutCommitted(
	ctx context.Context,
	requestMap map[string]interface{},
	execute func(ctx context.Context, body []byte) (*http.Response, error),
) bool {
	probe := maps.Clone(requestMap)
	delete(probe, "enforced_tokens")
	delete(probe, "min_tokens")
	delete(probe, "max_completion_tokens")
	probe["max_tokens"] = 1
	body, err := json.Marshal(probe)
	if err != nil {
		return false
	}
	resp, err := execute(ctx, body)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func tapeResult(body []byte, committed int, base BaseValidationResult) (ValidationResult, error) {
	var replay struct {
		Tape  *TapeSignals `json:"gonka_tape"`
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &replay); err != nil {
		return nil, err
	}
	if replay.Tape == nil {
		return nil, errNoTapeSignals
	}
	// A replay ends with the committed tokens where the model ends. Before them,
	// the output ran past the model's end; after them, in the EOS the inference
	// fork appends to a replay, the output was cut short of it.
	if replay.Tape.Steps < committed || replay.Usage.CompletionTokens > committed {
		return &DifferentLengthValidationResult{base}, nil
	}
	return &TapeValidationResult{BaseValidationResult: base, Signals: *replay.Tape}, nil
}
