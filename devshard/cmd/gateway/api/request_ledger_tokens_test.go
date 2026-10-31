package api

import (
	"testing"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/filters"
)

// Test flow:
//  1. Build a race outcome with a gateway input-token estimate and a winning attempt reporting its own prompt and completion token usage.
//  2. Call requestRecord on it.
//  3. Assert InputTokens keeps the gateway's tokenizer count with its source, and PromptTokens/WinnerOutputTokens carry the host-reported counts.
func TestTheRequestRowCarriesTheEstimateAndTheMeasuredPromptApart(t *testing.T) {
	t.Parallel()
	winner := engine.AttemptOutcome{
		Participant:           "participant-1",
		Nonce:                 7,
		Terminal:              engine.TerminalWon,
		UsagePromptTokens:     27_438,
		UsageCompletionTokens: 4_096,
	}
	outcome := engine.RaceOutcome{
		RequestID:         "req-1",
		Model:             "qwen",
		InputTokens:       50_791,
		InputTokensSource: filters.InputTokensTokenizerSampled,
		Succeeded:         true,
		WinnerNonce:       7,
		Attempts:          []engine.AttemptOutcome{winner},
	}

	record := requestRecord(outcome)

	if record.InputTokens != 50_791 || record.InputTokensSource != filters.InputTokensTokenizerSampled {
		t.Fatalf("input_tokens = %d from %q, want the gateway's own count from the tokenizer", record.InputTokens, record.InputTokensSource)
	}
	if record.PromptTokens != 27_438 {
		t.Fatalf("prompt_tokens = %d, want the count the host reported", record.PromptTokens)
	}
	if record.WinnerOutputTokens != 4_096 {
		t.Fatalf("winner_output_tokens = %d, want the count the host reported", record.WinnerOutputTokens)
	}
}

// Test flow:
//  1. Build a race outcome whose winning attempt reports no usage tokens but has logprob tokens counted off the stream.
//  2. Call requestRecord on it.
//  3. Assert WinnerOutputTokens and TotalOutputTokens fall back to the tokens counted off the stream.
func TestAHostThatReportedNoUsageStillHasItsOutputCounted(t *testing.T) {
	t.Parallel()
	outcome := engine.RaceOutcome{
		RequestID:   "req-1",
		Model:       "qwen",
		Succeeded:   true,
		WinnerNonce: 7,
		Attempts: []engine.AttemptOutcome{{
			Participant:   "participant-1",
			Nonce:         7,
			Terminal:      engine.TerminalWon,
			LogprobTokens: 4_096,
		}},
	}

	record := requestRecord(outcome)

	if record.WinnerOutputTokens != 4_096 {
		t.Fatalf("winner_output_tokens = %d, want the tokens the gateway counted off the stream", record.WinnerOutputTokens)
	}
	if record.TotalOutputTokens != 4_096 {
		t.Fatalf("total_output_tokens = %d, want the tokens the gateway counted off the stream", record.TotalOutputTokens)
	}
}

// Test flow:
//  1. Build a race outcome with a gateway input-token estimate and no source, whose winning attempt reports no usage.
//  2. Call requestRecord on it.
//  3. Assert PromptTokens stays zero rather than borrowing the estimate, and InputTokens still reports the estimate, marked bytes_estimate.
func TestAnUnreportedPromptIsNotFilledInFromTheEstimate(t *testing.T) {
	t.Parallel()
	outcome := engine.RaceOutcome{
		RequestID:   "req-1",
		Model:       "qwen",
		InputTokens: 50_791,
		Succeeded:   true,
		WinnerNonce: 7,
		Attempts:    []engine.AttemptOutcome{{Participant: "participant-1", Nonce: 7, Terminal: engine.TerminalWon}},
	}

	record := requestRecord(outcome)

	if record.PromptTokens != 0 {
		t.Fatalf("prompt_tokens = %d, want 0 when the host reported none", record.PromptTokens)
	}
	if record.InputTokens != 50_791 || record.InputTokensSource != filters.InputTokensBytesEstimate {
		t.Fatalf("input_tokens = %d from %q, want the estimate to stand on its own as %q", record.InputTokens, record.InputTokensSource, filters.InputTokensBytesEstimate)
	}
}
