package engine

import "testing"

// Test flow:
//  1. Build an SSE classifier and a fresh `attemptState`.
//  2. Feed it the same two-logprob-token chunk three times through `attempt.record`.
//  3. Assert `attempt.logprobTokens` totals 6.
func TestTheGatewayCountsTheTokensItStreamed(t *testing.T) {
	t.Parallel()
	classifier := newSSEClassifier(testBudget(1<<20, 1<<20, 1<<20), testParticipant, false, nil)
	attempt := &attemptState{}

	for range 3 {
		attempt.record(classifier.Classify([]byte(
			`data: {"choices":[{"delta":{"content":"hi"},"logprobs":{"content":[{"token":"h"},{"token":"i"}]}}]}` + "\n\n")))
	}

	if attempt.logprobTokens != 6 {
		t.Fatalf("counted %d tokens, want the 6 the stream carried", attempt.logprobTokens)
	}
}

// Test flow:
//  1. Build an `AttemptOutcome` with `LogprobTokens` set to 37 and no reported usage.
//  2. Assert `OutputTokens()` returns 37, the count from before the stream was cut.
func TestAnInterruptedStreamStillReportsWhatItProduced(t *testing.T) {
	t.Parallel()
	cut := AttemptOutcome{LogprobTokens: 37}

	if got := cut.OutputTokens(); got != 37 {
		t.Fatalf("OutputTokens = %d, want the 37 counted before the stream was cut", got)
	}
}

// Test flow:
//  1. Build an `AttemptOutcome` with `UsageCompletionTokens` 4096 and `LogprobTokens` 11.
//  2. Assert `OutputTokens()` returns the host's own reported count, 4096, not the logprob count.
func TestAHostThatReportsItsOwnUsageIsBelieved(t *testing.T) {
	t.Parallel()
	reported := AttemptOutcome{UsageCompletionTokens: 4_096, LogprobTokens: 11}

	if got := reported.OutputTokens(); got != 4_096 {
		t.Fatalf("OutputTokens = %d, want the host's own count", got)
	}
}

// Test flow:
//  1. Build an `AttemptOutcome` with `UsageCompletionTokens` 0 and `LogprobTokens` 4096.
//  2. Assert `OutputTokens()` falls back to the gateway's counted logprob tokens, 4096.
func TestAHostThatReportedNoUsageFallsBackToTheCount(t *testing.T) {
	t.Parallel()
	silent := AttemptOutcome{UsageCompletionTokens: 0, LogprobTokens: 4_096}

	if got := silent.OutputTokens(); got != 4_096 {
		t.Fatalf("OutputTokens = %d, want the tokens the gateway counted off the stream", got)
	}
}
