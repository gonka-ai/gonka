package engine

import (
	"testing"
)

// Test flow:
//  1. Build an SSE classifier.
//  2. Classify a content chunk with no usage, then a chunk carrying `usage.prompt_tokens` and `usage.completion_tokens`.
//  3. Assert the returned facts carry both `UsagePromptTokens` and `UsageCompletionTokens` as reported.
func TestBothHalvesOfAHostsUsageAreRead(t *testing.T) {
	t.Parallel()
	classifier := newSSEClassifier(testBudget(1<<20, 1<<20, 1<<20), testParticipant, false, nil)

	classifier.Classify([]byte(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"))
	facts := classifier.Classify([]byte(`data: {"choices":[],"usage":{"prompt_tokens":27438,"completion_tokens":4096}}` + "\n\n"))

	if facts.UsagePromptTokens != 27_438 {
		t.Fatalf("UsagePromptTokens = %d, want the prompt the host counted", facts.UsagePromptTokens)
	}
	if facts.UsageCompletionTokens != 4_096 {
		t.Fatalf("UsageCompletionTokens = %d, want the completion the host counted", facts.UsageCompletionTokens)
	}
}

// Test flow:
//  1. Build a fresh `attemptState`.
//  2. Record one chunk with both usage halves, an empty chunk, then another chunk with different usage halves.
//  3. Assert the attempt's latched usage keeps only the last chunk's paired prompt and completion counts.
func TestBothHalvesOfTheUsageLatchTogether(t *testing.T) {
	t.Parallel()
	attempt := &attemptState{}

	attempt.record(chunkFacts{UsagePromptTokens: 10, UsageCompletionTokens: 20})
	attempt.record(chunkFacts{})
	attempt.record(chunkFacts{UsagePromptTokens: 27_438, UsageCompletionTokens: 4_096})

	if attempt.usagePromptTokens != 27_438 || attempt.usageCompletionTokens != 4_096 {
		t.Fatalf("usage = %d/%d, want both halves from the same object",
			attempt.usagePromptTokens, attempt.usageCompletionTokens)
	}
}

// Test flow:
//  1. Build an SSE classifier.
//  2. Classify one read containing three SSE events, each with a growing `usage.completion_tokens` count.
//  3. Assert the returned facts carry the last event's completion and prompt token counts, not an earlier one.
func TestRunningUsageIsReadAtItsLastValueWithinOneRead(t *testing.T) {
	t.Parallel()
	classifier := newSSEClassifier(testBudget(1<<20, 1<<20, 1<<20), testParticipant, false, nil)

	facts := classifier.Classify([]byte(
		`data: {"choices":[{"delta":{"content":"a"}}],"usage":{"prompt_tokens":27438,"completion_tokens":1}}` + "\n\n" +
			`data: {"choices":[{"delta":{"content":"b"}}],"usage":{"prompt_tokens":27438,"completion_tokens":2}}` + "\n\n" +
			`data: {"choices":[],"usage":{"prompt_tokens":27438,"completion_tokens":4096}}` + "\n\n"))

	if facts.UsageCompletionTokens != 4_096 {
		t.Fatalf("UsageCompletionTokens = %d, want 4096 -- the count the stream ended on, not the one it opened with",
			facts.UsageCompletionTokens)
	}
	if facts.UsagePromptTokens != 27_438 {
		t.Fatalf("UsagePromptTokens = %d, want the prompt the host counted", facts.UsagePromptTokens)
	}
}
