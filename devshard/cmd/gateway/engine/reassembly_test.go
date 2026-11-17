package engine

import (
	"bytes"
	"strconv"
	"testing"

	"devshard/cmd/gateway/filters"
)

func newTestClassifier(emptyAnswerCountsTokens bool, overflow func()) *sseClassifier {
	return newSSEClassifier(testBudget(1<<20, 1<<20, 1<<20), testParticipant, emptyAnswerCountsTokens, overflow)
}

// Test flow:
//  1. For each table case's thinking-budget flag and raw SSE chunk (content, role-only, a plain error, a context-limit refusal, a tool refusal, thinking-budget usage on/off route, an empty-stop-with-usage crowning on/off route), classify it with a fresh `sseClassifier`.
//  2. Assert the returned `chunkFacts` match the case's expectation.
func TestClassifierMapsOneChunkToItsFacts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                    string
		emptyAnswerCountsTokens bool
		chunk                   string
		want                    chunkFacts
	}{
		{
			name:  "content_chunk",
			chunk: `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n",
			want:  chunkFacts{Content: true, ContentSource: "delta.content"},
		},
		{
			name:  "role_only_chunk_is_neither_content_nor_error",
			chunk: `data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n",
			want:  chunkFacts{},
		},
		{
			name:  "error_event_counts_as_a_chunk_but_never_crowns",
			chunk: `data: {"error":{"code":500,"message":"backend failed","type":"server_error"}}` + "\n\n",
			want: chunkFacts{
				Error:        true,
				ErrorSource:  "error.server_error",
				ErrorCode:    "500",
				ErrorType:    "server_error",
				ErrorMessage: "backend failed",
				ErrorPayload: `{"error":{"code":500,"message":"backend failed","type":"server_error"}}`,
			},
		},
		{
			name:  "context_limit_refusal_is_retriable_not_an_error_chunk",
			chunk: `data: {"error":{"code":400,"message":"` + vllmContextTotalMessage + `","type":"BadRequestError"}}` + "\n\n",
			want: chunkFacts{
				ErrorSource:       "error.BadRequestError",
				ErrorCode:         "400",
				ErrorType:         "BadRequestError",
				ErrorMessage:      vllmContextTotalMessage,
				ErrorPayload:      `{"error":{"code":400,"message":"` + vllmContextTotalMessage + `","type":"BadRequestError"}}`,
				CapabilityRefused: true,
				Capability:        CapabilitySignal{ContextLimit: 40960, ContextRequested: 41200},
			},
		},
		{
			name:  "tool_refusal_is_retriable_not_an_error_chunk",
			chunk: `data: {"object":"error","message":"` + filters.ToolChoiceUnsupportedMessage + `","type":"BadRequestError"}` + "\n\n",
			want: chunkFacts{
				ErrorSource:       "error.BadRequestError",
				ErrorType:         "BadRequestError",
				ErrorMessage:      filters.ToolChoiceUnsupportedMessage,
				ErrorPayload:      `{"object":"error","message":"` + filters.ToolChoiceUnsupportedMessage + `","type":"BadRequestError"}`,
				CapabilityRefused: true,
				Capability:        CapabilitySignal{ToolsUnsupported: true},
			},
		},
		{
			name:                    "usage_burns_tokens_on_a_thinking_budget_route",
			emptyAnswerCountsTokens: true,
			chunk:                   `data: {"choices":[{"delta":{}}],"usage":{"completion_tokens":90}}` + "\n\n",
			want:                    chunkFacts{UsageCompletionTokens: 90, TokensBurned: true},
		},
		{
			name:  "usage_off_the_thinking_budget_route_burns_nothing",
			chunk: `data: {"choices":[{"delta":{}}],"usage":{"completion_tokens":90}}` + "\n\n",
			want:  chunkFacts{UsageCompletionTokens: 90},
		},
		{
			name:                    "an_empty_stop_with_usage_crowns_on_a_thinking_budget_route",
			emptyAnswerCountsTokens: true,
			chunk:                   `data: {"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":90}}` + "\n\n",
			want: chunkFacts{
				Content:               true,
				ContentSource:         "message.empty_stop_completion_tokens",
				UsageCompletionTokens: 90,
				TokensBurned:          true,
				FinishReason:          "stop",
			},
		},
		{
			name:  "an_empty_stop_with_usage_crowns_nobody_off_that_route",
			chunk: `data: {"choices":[{"message":{"content":""},"finish_reason":"stop"}],"usage":{"completion_tokens":90}}` + "\n\n",
			want:  chunkFacts{UsageCompletionTokens: 90, FinishReason: "stop"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			classifier := newTestClassifier(testCase.emptyAnswerCountsTokens, nil)
			defer classifier.Release()

			got := classifier.Classify([]byte(testCase.chunk))

			if got != testCase.want {
				t.Errorf("Classify() = %+v, want %+v", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Classify a chunk split mid-line: a head with no terminator, then the tail that completes it.
//  2. Assert the head is not read as content.
//  3. Assert the completed tail is read as content from `delta.content`.
func TestClassifierHoldsASplitEventUntilItsLineCompletes(t *testing.T) {
	t.Parallel()
	classifier := newTestClassifier(false, nil)
	defer classifier.Release()

	head := classifier.Classify([]byte(`data: {"choices":[{"delta":{"con`))
	tail := classifier.Classify([]byte(`tent":"hi"}}]}` + "\n\n"))

	if head.Content {
		t.Error("a half-delivered event was classified as content")
	}
	if !tail.Content || tail.ContentSource != "delta.content" {
		t.Errorf("completing chunk = %+v, want content from delta.content", tail)
	}
}

// Test flow:
//  1. Classify one SSE event fed one byte at a time.
//  2. Count how many of those single-byte reads report content.
//  3. Assert the event is counted as content exactly once, on completion.
func TestClassifierCountsAReassembledEventExactlyOnce(t *testing.T) {
	t.Parallel()
	event := []byte(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n")
	classifier := newTestClassifier(false, nil)
	defer classifier.Release()

	contentChunks := 0
	for index := range event {
		if classifier.Classify(event[index : index+1]).Content {
			contentChunks++
		}
	}

	if contentChunks != 1 {
		t.Errorf("content chunks = %d, want 1", contentChunks)
	}
}

// Test flow:
//  1. Classify a content event with no trailing newline terminator.
//  2. Assert it is not read as content before the stream closes.
//  3. Call `Flush` and assert it now reads as content from `delta.content`.
func TestClassifierFlushReadsTheNewlineLessFinalEvent(t *testing.T) {
	t.Parallel()
	classifier := newTestClassifier(false, nil)
	defer classifier.Release()

	streamed := classifier.Classify([]byte(`data: {"choices":[{"delta":{"content":"hi"}}]}`))
	flushed := classifier.Flush()

	if streamed.Content {
		t.Error("an unterminated event was classified before the stream closed")
	}
	if !flushed.Content || flushed.ContentSource != "delta.content" {
		t.Errorf("Flush() = %+v, want content from delta.content", flushed)
	}
}

// Test flow:
//  1. Classify an unterminated chunk head and assert its bytes are charged to the participant's carry budget.
//  2. Release the classifier.
//  3. Assert the charge is fully refunded.
func TestClassifierReleaseRefundsTheHeldFragment(t *testing.T) {
	t.Parallel()
	budget := testBudget(1<<20, 1<<20, 1<<20)
	classifier := newSSEClassifier(budget, testParticipant, false, nil)

	classifier.Classify([]byte(`data: {"choices":[`))
	assertUsage(t, budget, testParticipant, 18, 18)

	classifier.Release()

	assertUsage(t, budget, testParticipant, 0, 0)
}

// Test flow:
//  1. Build a classifier with a tiny attempt cap and an overflow callback.
//  2. Classify the same oversized fragment twice, then a normal complete chunk.
//  3. Assert the overflow callback fired exactly once.
//  4. Assert classification still resumes and reports content after the cap trip.
func TestClassifierReportsAnOverflowOnceAndKeepsClassifying(t *testing.T) {
	t.Parallel()
	overflows := 0
	classifier := newSSEClassifier(testBudget(8, 1<<20, 1<<20), testParticipant, false, func() { overflows++ })
	defer classifier.Release()

	oversized := []byte(`data: {"choices":[{"delta":{"content":"much longer than the attempt cap"`)
	classifier.Classify(oversized)
	classifier.Classify(oversized)
	facts := classifier.Classify([]byte(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"))

	if overflows != 1 {
		t.Errorf("overflow reports = %d, want 1", overflows)
	}
	if !facts.Content {
		t.Error("classification did not resume after the cap trip")
	}
}

// Test flow:
//  1. For every fixture in `sseFixtures` and every chunk size in a table from 1 to 8192 bytes, replay the fixture body split into chunks of that size.
//  2. Assert the replayed state matches the fixture's expected state regardless of how it was chunked.
func TestClassifierReadsTheFixtureCorpusIdenticallyAtEveryChunkSize(t *testing.T) {
	t.Parallel()
	chunkSizes := []int{1, 2, 3, 7, 64, 256, 1024, 4096, 8192}
	for _, fixture := range sseFixtures {
		for _, chunkSize := range chunkSizes {
			t.Run(fixture.file+"/chunk="+strconv.Itoa(chunkSize), func(t *testing.T) {
				t.Parallel()
				body := readFixture(t, fixture.file)

				state := replayStream(t, false, true, fixedChunks(body, chunkSize)...)

				assertFixtureState(t, fixture, state)
			})
		}
	}
}

// Test flow:
//  1. Classify a content chunk.
//  2. Assert the classifier did not mutate the caller's byte slice.
func TestClassifierPreservesTheChunkItWasGiven(t *testing.T) {
	t.Parallel()
	chunk := []byte(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n")
	original := bytes.Clone(chunk)
	classifier := newTestClassifier(false, nil)
	defer classifier.Release()

	classifier.Classify(chunk)

	if !bytes.Equal(chunk, original) {
		t.Errorf("Classify mutated its input: %q", chunk)
	}
}
