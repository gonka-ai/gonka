package engine

import (
	"bytes"
	"context"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"devshard/cmd/gateway/filters"
	"devshard/transport"
)

const (
	kimiModel = "moonshotai/Kimi-K2.6"
	qwenModel = "Qwen/Qwen3-235B-A22B-Instruct-2507"
)

// Test flow:
//  1. Build a table of SSE chunk bodies, each naming the field that should be read as content (delta or message content, reasoning, reasoning_content, tool_calls, or a stop finish reason with completion tokens on a thinking-budget route) or an input expected to yield none (empty body, malformed JSON, non-data lines, an empty tool_calls array).
//  2. Run `contentSource` on the case's body and thinking-budget flag.
//  3. Assert the returned source equals the case's wantSource.
//  4. Assert ok is true exactly when wantSource is non-empty.
func TestContentSource(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                    string
		body                    string
		emptyAnswerCountsTokens bool
		wantSource              string
	}{
		{name: "empty", body: ""},
		{name: "done_only", body: "data: [DONE]\n\n"},
		{name: "role_chunk_only", body: `data: {"id":"a","choices":[{"delta":{"role":"assistant"},"index":0,"finish_reason":null}]}` + "\n\n"},
		{name: "finish_only", body: `data: {"id":"a","choices":[{"delta":{},"index":0,"finish_reason":"stop"}]}` + "\n\n"},
		{
			name:                    "empty_stop_with_completion_tokens_on_a_thinking_budget_route",
			body:                    `data: {"id":"a","choices":[{"message":{"content":""},"index":0,"finish_reason":"stop"}],"usage":{"completion_tokens":1}}` + "\n\n",
			emptyAnswerCountsTokens: true,
			wantSource:              "message.empty_stop_completion_tokens",
		},
		{
			name: "empty_stop_with_completion_tokens_off_a_thinking_budget_route",
			body: `data: {"id":"a","choices":[{"message":{"content":""},"index":0,"finish_reason":"stop"}],"usage":{"completion_tokens":1}}` + "\n\n",
		},
		{
			name:                    "empty_stop_without_completion_tokens",
			body:                    `data: {"id":"a","choices":[{"message":{"content":""},"index":0,"finish_reason":"stop"}],"usage":{"completion_tokens":0}}` + "\n\n",
			emptyAnswerCountsTokens: true,
		},
		{
			name:       "delta_content",
			body:       `data: {"id":"a","choices":[{"delta":{"content":"Hello"},"index":0}]}` + "\n\n",
			wantSource: "delta.content",
		},
		{
			name:       "delta_reasoning_content",
			body:       `data: {"id":"a","choices":[{"delta":{"reasoning_content":"hmm"},"index":0}]}` + "\n\n",
			wantSource: "delta.reasoning_content",
		},
		{
			name:       "delta_reasoning",
			body:       `data: {"id":"a","choices":[{"delta":{"reasoning":"hmm"},"index":0}]}` + "\n\n",
			wantSource: "delta.reasoning",
		},
		{
			name:       "delta_tool_calls",
			body:       `data: {"id":"a","choices":[{"delta":{"tool_calls":[{"id":"x"}]},"index":0}]}` + "\n\n",
			wantSource: "delta.tool_calls",
		},
		{
			name: "delta_tool_calls_empty_array",
			body: `data: {"id":"a","choices":[{"delta":{"tool_calls":[]},"index":0}]}` + "\n\n",
		},
		{
			name:       "delta_content_precedence_over_reasoning",
			body:       `data: {"choices":[{"delta":{"content":"hi","reasoning_content":"think"}}]}` + "\n\n",
			wantSource: "delta.content",
		},
		{
			name:       "message_content_convertible",
			body:       `data: {"choices":[{"message":{"content":"stub"}}],"usage":{}}` + "\n\n",
			wantSource: "message.content",
		},
		{
			name:       "message_reasoning_convertible",
			body:       `data: {"choices":[{"message":{"reasoning":"stub"}}]}` + "\n\n",
			wantSource: "message.reasoning",
		},
		{
			name:       "message_reasoning_content_convertible",
			body:       `data: {"choices":[{"message":{"reasoning_content":"stub"}}]}` + "\n\n",
			wantSource: "message.reasoning_content",
		},
		{
			name:       "message_tool_calls_convertible",
			body:       `data: {"choices":[{"message":{"tool_calls":[{"id":"x"}]}}]}` + "\n\n",
			wantSource: "message.tool_calls",
		},
		{
			name: "message_tool_calls_empty_array",
			body: `data: {"choices":[{"message":{"tool_calls":[]}}]}` + "\n\n",
		},
		{
			name: "completion_text_field_rejected",
			body: `data: {"choices":[{"text":"abc"}]}` + "\n\n",
		},
		{
			name:       "role_then_content_in_one_write",
			body:       `data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n" + `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n",
			wantSource: "delta.content",
		},
		{name: "malformed_json", body: "data: {not_json}\n\n"},
		{
			name: "openai_error_not_content",
			body: `data: {"error":{"code":400,"message":"bad request","type":"BadRequestError"}}` + "\n\n",
		},
		{name: "non_data_lines_ignored", body: "event: ping\nid: 42\n\n"},
		{name: "delta_empty_string", body: `data: {"choices":[{"delta":{"content":""}}]}` + "\n\n"},
		{
			name:       "content_beside_a_non_finite_logprob",
			body:       `data: {"choices":[{"message":{"content":"stub"},"logprobs":{"content":[{"logprob":-Infinity,"top_logprobs":[]}]}}]}` + "\n\n",
			wantSource: "message.content",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			source, ok := contentSource([]byte(testCase.body), testCase.emptyAnswerCountsTokens)
			if source != testCase.wantSource {
				t.Errorf("contentSource = %q, want %q", source, testCase.wantSource)
			}
			if ok != (testCase.wantSource != "") {
				t.Errorf("contentSource ok = %v, want %v", ok, testCase.wantSource != "")
			}
		})
	}
}

// Test flow:
//  1. Build one SSE chunk whose payload carries both delta content and an error object.
//  2. Classify the chunk with `classifyChunk`.
//  3. Assert the signal's ContentSource is non-empty.
//  4. Assert the signal's Error.Message is the error that rode with the content.
func TestAnErrorIsSeenEvenInAChunkThatAlsoCarriedContent(t *testing.T) {
	t.Parallel()
	chunk := []byte(`data: {"choices":[{"delta":{"content":"hi"}}],"error":{"message":"boom","type":"server_error"}}` + "\n\n")

	signal := classifyChunk(chunk, false)

	if signal.ContentSource == "" {
		t.Fatal("the content in the chunk was not seen")
	}
	if signal.Error.Message != "boom" {
		t.Fatalf("Error.Message = %q, want the failure that rode with the content", signal.Error.Message)
	}
}

// Test flow:
//  1. Build a table of SSE chunk bodies covering OpenAI-shaped errors (nested and flat, with and without a type, a null or string code, a bare-string message, a bare string with a sibling error_type), non-error inputs, and an error riding alongside content.
//  2. Run `errorPayload` on each case's body.
//  3. Assert ok is true exactly when the case names a wantSource.
//  4. Assert the returned failure's Source, Code, Type, and Message match the case's expectations.
func TestErrorPayload(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		body        string
		wantSource  string
		wantCode    string
		wantType    string
		wantMessage string
	}{
		{
			name:        "openai_error_with_type",
			body:        `data: {"error":{"code":400,"message":"bad request","type":"BadRequestError"}}` + "\n\n",
			wantSource:  "error.BadRequestError",
			wantCode:    "400",
			wantType:    "BadRequestError",
			wantMessage: "bad request",
		},
		{
			name:        "flat_openai_error_with_type",
			body:        `data: {"code":400,"object":"error","message":"context too long","type":"BadRequestError"}` + "\n\n",
			wantSource:  "error.BadRequestError",
			wantCode:    "400",
			wantType:    "BadRequestError",
			wantMessage: "context too long",
		},
		{
			name:        "openai_error_without_type",
			body:        `data: {"error":{"code":500,"message":"backend failed"}}` + "\n\n",
			wantSource:  "error",
			wantCode:    "500",
			wantMessage: "backend failed",
		},
		{
			name:        "null_code_is_empty_not_nil_literal",
			body:        `data: {"error":{"code":null,"message":"boom","type":"server_error"}}` + "\n\n",
			wantSource:  "error.server_error",
			wantType:    "server_error",
			wantMessage: "boom",
		},
		{
			name:        "flat_error_null_code",
			body:        `data: {"object":"error","code":null,"message":"boom","type":"server_error"}` + "\n\n",
			wantSource:  "error.server_error",
			wantType:    "server_error",
			wantMessage: "boom",
		},
		{
			name:        "string_code",
			body:        `data: {"error":{"message":"boom","type":"server_error","code":"internal"}}` + "\n\n",
			wantSource:  "error.server_error",
			wantCode:    "internal",
			wantType:    "server_error",
			wantMessage: "boom",
		},
		{
			name:        "error_as_a_bare_string",
			body:        `data: {"error":"boom"}` + "\n\n",
			wantSource:  "error",
			wantMessage: "boom",
		},
		{
			name:        "error_as_a_bare_string_with_a_sibling_type",
			body:        `data: {"error":"Request failed during generation","error_type":"generation"}` + "\n\n",
			wantSource:  "error.generation",
			wantType:    "generation",
			wantMessage: "Request failed during generation",
		},
		{
			name:       "flat_shape_without_message",
			body:       `data: {"object":"error","type":"BadRequestError"}` + "\n\n",
			wantSource: "error.BadRequestError",
			wantType:   "BadRequestError",
		},
		{
			name:        "an_error_riding_with_content_in_one_payload",
			body:        `data: {"choices":[{"delta":{"content":"hi"}}],"error":{"message":"boom"}}` + "\n\n",
			wantSource:  "error",
			wantMessage: "boom",
		},
		{name: "done_only", body: "data: [DONE]\n\n"},
		{name: "content_not_error", body: `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"},
		{name: "empty_error_string_is_not_a_failure", body: `data: {"error":""}` + "\n\n"},
		{name: "null_error_is_not_a_failure", body: `data: {"error":null}` + "\n\n"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			failure, ok := errorPayload([]byte(testCase.body))
			if ok != (testCase.wantSource != "") {
				t.Fatalf("errorPayload ok = %v, want %v", ok, testCase.wantSource != "")
			}
			if failure.Source != testCase.wantSource {
				t.Errorf("Source = %q, want %q", failure.Source, testCase.wantSource)
			}
			if failure.Code != testCase.wantCode {
				t.Errorf("Code = %q, want %q", failure.Code, testCase.wantCode)
			}
			if failure.Type != testCase.wantType {
				t.Errorf("Type = %q, want %q", failure.Type, testCase.wantType)
			}
			if failure.Message != testCase.wantMessage {
				t.Errorf("Message = %q, want %q", failure.Message, testCase.wantMessage)
			}
		})
	}
}

// Test flow:
//  1. Build one SSE chunk carrying an error event.
//  2. Extract its failure with `errorPayload`.
//  3. Overwrite every byte of the original chunk buffer.
//  4. Assert the extracted Payload still reads the original error text, unaffected by the mutation.
func TestErrorPayloadCopiesTheEventBytes(t *testing.T) {
	t.Parallel()
	body := []byte(`data: {"error":{"message":"boom","type":"server_error"}}` + "\n\n")

	failure, ok := errorPayload(body)

	if !ok {
		t.Fatal("errorPayload found nothing")
	}
	want := `{"error":{"message":"boom","type":"server_error"}}`
	if failure.Payload != want {
		t.Fatalf("Payload = %q, want %q", failure.Payload, want)
	}
	for index := range body {
		body[index] = 'x'
	}
	if failure.Payload != want {
		t.Errorf("Payload aliases the caller's buffer: %q", failure.Payload)
	}
}

// Test flow:
//  1. Build a table of SSE chunk bodies covering present, zero, absent, and null completion-token usage, a malformed event that must be skipped in favor of a later valid one, and usage alongside a non-finite logprob value.
//  2. Run `usageCompletionTokens` on each case's body.
//  3. Assert the returned token count matches the case's wantTokens.
//  4. Assert ok is true exactly when wantTokens is greater than zero.
func TestUsageCompletionTokens(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		body       string
		wantTokens int64
	}{
		{
			name:       "usage_with_completion_tokens",
			body:       `data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":100,"total_tokens":112}}` + "\n\n",
			wantTokens: 100,
		},
		{name: "usage_zero_completion_tokens", body: `data: {"usage":{"prompt_tokens":12,"completion_tokens":0,"total_tokens":12}}` + "\n\n"},
		{name: "usage_absent", body: `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"},
		{name: "done_marker_only", body: "data: [DONE]\n\n"},
		{name: "empty_input", body: ""},
		{
			name:       "malformed_json_skipped",
			body:       "data: {not json}\n\ndata: {\"usage\":{\"completion_tokens\":42}}\n\n",
			wantTokens: 42,
		},
		{name: "usage_null", body: `data: {"choices":[],"usage":null}` + "\n\n"},
		{
			name:       "usage_beside_a_non_finite_logprob",
			body:       `data: {"choices":[{"message":{"content":"stub"},"logprobs":{"content":[{"logprob":-Infinity}]}}],"usage":{"completion_tokens":40}}` + "\n\n",
			wantTokens: 40,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			tokens, ok := usageCompletionTokens([]byte(testCase.body))
			if tokens != testCase.wantTokens {
				t.Errorf("tokens = %d, want %d", tokens, testCase.wantTokens)
			}
			if ok != (testCase.wantTokens > 0) {
				t.Errorf("ok = %v, want %v", ok, testCase.wantTokens > 0)
			}
		})
	}
}

// Test flow:
//  1. Build a table of error messages covering a tool-choice refusal, context-length refusals (with trailing text, at the end of the message, and matched case-insensitively), a plain model error, a marker without digits, and an empty message.
//  2. Run `ParseCapabilityError` on each case's message.
//  3. Assert Refused() matches the case's wantRefused.
//  4. Assert Retriable() matches the case's wantRetriable.
//  5. Assert ContextLimit matches the case's wantLimit.
func TestParseCapabilityErrorRecognisesARefusal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		message       string
		wantRefused   bool
		wantRetriable bool
		wantLimit     uint64
	}{
		{name: "tool_choice_unsupported", message: filters.ToolChoiceUnsupportedMessage, wantRefused: true, wantRetriable: true},
		{
			name:        "context_length_with_trailing_text",
			message:     "This model's maximum context length is 131072 tokens. However, you requested 150000 tokens.",
			wantRefused: true,
			wantLimit:   131072,
		},
		{
			name:        "context_length_at_end_of_message",
			message:     "This model's maximum context length is 131072",
			wantRefused: true,
			wantLimit:   131072,
		},
		{
			name:        "marker_matched_case_insensitively",
			message:     "Maximum Context Length Is 4096 tokens",
			wantRefused: true,
			wantLimit:   4096,
		},
		{name: "plain_model_error", message: "plain model error"},
		{name: "marker_without_digits", message: "maximum context length is unknown"},
		{name: "empty", message: ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := ParseCapabilityError(testCase.message).Refused(); got != testCase.wantRefused {
				t.Errorf("Refused() = %v, want %v", got, testCase.wantRefused)
			}
			if got := ParseCapabilityError(testCase.message).Retriable(); got != testCase.wantRetriable {
				t.Errorf("Retriable() = %v, want %v", got, testCase.wantRetriable)
			}
			if got := ParseCapabilityError(testCase.message).ContextLimit; got != testCase.wantLimit {
				t.Errorf("ContextLimit = %d, want %d", got, testCase.wantLimit)
			}
		})
	}
}

// replayStream drives one attempt's real classifier and accumulator over chunks and returns the resulting state.
func replayStream(t *testing.T, emptyAnswerCountsTokens bool, receipted bool, chunks ...[]byte) *attemptState {
	t.Helper()
	classifier := newTestClassifier(emptyAnswerCountsTokens, nil)
	defer classifier.Release()

	state := &attemptState{}
	if receipted {
		state.receiptTime = testEpoch
	}
	for _, chunk := range chunks {
		state.record(classifier.Classify(chunk))
	}
	state.classify(context.Background(), AttemptSpec{Model: qwenModel, Classifier: classifier}, nil)
	return state
}

func fixedChunks(body []byte, size int) [][]byte {
	chunks := make([][]byte, 0, len(body)/size+1)
	for start := 0; start < len(body); start += size {
		chunks = append(chunks, body[start:min(start+size, len(body))])
	}
	return chunks
}

// Test flow:
//  1. Build a table of one-chunk SSE bodies: a plain server error, a tool-choice capability refusal, a context-length capability refusal, and a content-bearing chunk.
//  2. Replay each body through `replayStream` (the real classifier and `attemptState` accumulator) on a receipted attempt.
//  3. Assert the resulting contentSource, contentChunks, and errorSource match the case's expectations.
//  4. Assert the terminal matches the case's wantTerminal.
func TestAccumulatorCountsErrorEventsWithoutCrowningThem(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		body            string
		wantSource      string
		wantChunks      int64
		wantErrorSource string
		wantTerminal    Terminal
	}{
		{
			name:            "server_error_counts_but_does_not_crown",
			body:            `data: {"error":{"code":500,"message":"backend failed","type":"server_error"}}` + "\n\n",
			wantChunks:      1,
			wantErrorSource: "error.server_error",
			wantTerminal:    TerminalErrorStream,
		},
		{
			name:            "tool_choice_refusal_neither_counts_nor_crowns",
			body:            `data: {"error":{"code":400,"message":"` + filters.ToolChoiceUnsupportedMessage + `","type":"BadRequestError"}}` + "\n\n",
			wantErrorSource: "error.BadRequestError",
			wantTerminal:    TerminalCapabilityRefused,
		},
		{
			name:            "context_length_refusal_neither_counts_nor_crowns",
			body:            `data: {"error":{"code":400,"message":"This model's maximum context length is 131072 tokens.","type":"BadRequestError"}}` + "\n\n",
			wantErrorSource: "error.BadRequestError",
			wantTerminal:    TerminalCapabilityRefused,
		},
		{
			name:         "content_crowns",
			body:         `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n",
			wantSource:   "delta.content",
			wantChunks:   1,
			wantTerminal: TerminalLost,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			state := replayStream(t, false, true, []byte(testCase.body))

			if state.contentSource != testCase.wantSource {
				t.Errorf("contentSource = %q, want %q", state.contentSource, testCase.wantSource)
			}
			if state.contentChunks != testCase.wantChunks {
				t.Errorf("contentChunks = %d, want %d", state.contentChunks, testCase.wantChunks)
			}
			if state.errorSource != testCase.wantErrorSource {
				t.Errorf("errorSource = %q, want %q", state.errorSource, testCase.wantErrorSource)
			}
			if state.terminal != testCase.wantTerminal {
				t.Errorf("terminal = %v, want %v", state.terminal, testCase.wantTerminal)
			}
		})
	}
}

// Test flow:
//  1. Build a table of pre-accumulated `attemptState` values covering the receipt, content, error, capability-refusal, and burned-tokens combinations that decide a terminal.
//  2. Run `state.classify` on each case's state with a fake classifier and no dispatch error.
//  3. Assert the resulting terminal matches the case's want.
func TestTerminalLadderOverAccumulatedFacts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		state attemptState
		want  Terminal
	}{
		{name: "never_receipted", want: TerminalNoReceipt},
		{name: "no_receipt_with_content", state: attemptState{contentChunks: 1}, want: TerminalNoReceipt},
		{name: "receipt_without_content", state: attemptState{receiptTime: testEpoch}, want: TerminalEmptyStream},
		{name: "receipt_with_content", state: attemptState{receiptTime: testEpoch, contentChunks: 1}, want: TerminalLost},
		{
			name:  "error_stream_is_never_empty",
			state: attemptState{receiptTime: testEpoch, errorSource: "error.server_error"},
			want:  TerminalErrorStream,
		},
		{
			name:  "capability_refusal_is_its_own_terminal",
			state: attemptState{receiptTime: testEpoch, errorSource: "error.BadRequestError", capabilityRefused: true},
			want:  TerminalCapabilityRefused,
		},
		{
			name:  "burned_tokens_separate_the_model_from_the_host",
			state: attemptState{receiptTime: testEpoch, usageCompletionTokens: 100, tokensBurned: true},
			want:  TerminalBurnEmpty,
		},
		{
			name:  "an_error_stream_is_never_burn_empty",
			state: attemptState{receiptTime: testEpoch, errorSource: "error.server_error", tokensBurned: true},
			want:  TerminalErrorStream,
		},
		{
			name:  "content_is_never_burn_empty",
			state: attemptState{receiptTime: testEpoch, contentChunks: 1, tokensBurned: true},
			want:  TerminalLost,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			state := testCase.state
			state.classify(context.Background(), AttemptSpec{Classifier: &fakeClassifier{}}, nil)

			if state.terminal != testCase.want {
				t.Errorf("terminal = %v, want %v", state.terminal, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build one SSE chunk carrying only usage.completion_tokens and a length finish reason, no content.
//  2. Replay it through `replayStream` once for a thinking-budget model and once for a plain model.
//  3. Assert usageCompletionTokens is recorded and contentChunks stays 0 in both cases.
//  4. Assert the thinking-budget route classifies as TerminalBurnEmpty and the other route as TerminalEmptyStream.
func TestBurnEmptyIsOnlyReachableOnAThinkingBudgetRoute(t *testing.T) {
	t.Parallel()
	const usageOnly = `data: {"choices":[{"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":12,"completion_tokens":100,"total_tokens":112}}` + "\n\n"
	cases := []struct {
		name                    string
		emptyAnswerCountsTokens bool
		want                    Terminal
	}{
		{name: "thinking_budget_route_burned_the_tokens", emptyAnswerCountsTokens: true, want: TerminalBurnEmpty},
		{name: "any_other_route_answered_with_nothing", emptyAnswerCountsTokens: false, want: TerminalEmptyStream},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			state := replayStream(t, testCase.emptyAnswerCountsTokens, true, []byte(usageOnly))

			if state.usageCompletionTokens != 100 {
				t.Errorf("usageCompletionTokens = %d, want 100", state.usageCompletionTokens)
			}
			if state.contentChunks != 0 {
				t.Errorf("contentChunks = %d, want 0: a usage-only chunk carries no content", state.contentChunks)
			}
			if state.terminal != testCase.want {
				t.Errorf("terminal = %v, want %v", state.terminal, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Replay four chunks through `replayStream`: a reasoning delta, a content delta, then two different error events.
//  2. Assert contentSource keeps the first content-bearing field seen (delta.reasoning), not the later content chunk.
//  3. Assert errorSource and errorMessage keep the first error ("error.server_error"/"first"), not the second.
//  4. Assert errorPayload holds the first error event's raw bytes.
//  5. Assert contentChunks counts all four chunks, since each carried either content or an error.
func TestFirstContentAndFirstErrorAreTheOnesRecorded(t *testing.T) {
	t.Parallel()
	state := replayStream(t, false, true,
		[]byte(`data: {"choices":[{"delta":{"reasoning":"think"}}]}`+"\n\n"),
		[]byte(`data: {"choices":[{"delta":{"content":"hi"}}]}`+"\n\n"),
		[]byte(`data: {"error":{"message":"first","type":"server_error"}}`+"\n\n"),
		[]byte(`data: {"error":{"message":"second","type":"other_error"}}`+"\n\n"),
	)

	if state.contentSource != "delta.reasoning" {
		t.Errorf("contentSource = %q, want %q", state.contentSource, "delta.reasoning")
	}
	if state.errorSource != "error.server_error" || state.errorMessage != "first" {
		t.Errorf("error = %q/%q, want error.server_error/first", state.errorSource, state.errorMessage)
	}
	if state.errorPayload != `{"error":{"message":"first","type":"server_error"}}` {
		t.Errorf("errorPayload = %q, want the first error event verbatim", state.errorPayload)
	}
	if state.contentChunks != 4 {
		t.Errorf("contentChunks = %d, want 4", state.contentChunks)
	}
}

type fixtureExpectation struct {
	file            string
	wantSource      string
	wantErrorSource string
	wantUsage       int64
	newlineLess     bool
}

var sseFixtures = []fixtureExpectation{
	{file: "content_stream.sse", wantSource: "delta.content", wantUsage: 2},
	{file: "tool_calls_stream.sse", wantSource: "delta.tool_calls", wantUsage: 20},
	{file: "newlineless_final_content.sse", wantSource: "delta.content", newlineLess: true},
	{file: "newlineless_final_error.sse", wantErrorSource: "error.server_error", newlineLess: true},
}

func readFixture(t *testing.T, file string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "filters", "testdata", "sse", file))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	if bytes.HasSuffix(body, []byte("\n")) == fixtureIsNewlineLess(file) {
		t.Fatalf("fixture %s no longer matches its recorded line ending", file)
	}
	return body
}

func fixtureIsNewlineLess(file string) bool {
	for _, fixture := range sseFixtures {
		if fixture.file == file {
			return fixture.newlineLess
		}
	}
	return false
}

func assertFixtureState(t *testing.T, fixture fixtureExpectation, state *attemptState) {
	t.Helper()
	if state.contentSource != fixture.wantSource {
		t.Errorf("contentSource = %q, want %q", state.contentSource, fixture.wantSource)
	}
	if state.errorSource != fixture.wantErrorSource {
		t.Errorf("errorSource = %q, want %q", state.errorSource, fixture.wantErrorSource)
	}
	if state.usageCompletionTokens != fixture.wantUsage {
		t.Errorf("usageCompletionTokens = %d, want %d", state.usageCompletionTokens, fixture.wantUsage)
	}
	if state.terminal == TerminalEmptyStream || state.terminal == TerminalBurnEmpty {
		t.Errorf("terminal = %v, want a stream that carried something", state.terminal)
	}
}

// Test flow:
//  1. For each recorded SSE fixture file, read its raw bytes.
//  2. Replay the whole file as a single chunk through `replayStream`.
//  3. Assert the resulting state's contentSource, errorSource, and usageCompletionTokens match the fixture's recorded expectations, and that the stream was not classified as empty.
func TestFixtureStreamsClassifyWholeBlob(t *testing.T) {
	t.Parallel()
	for _, fixture := range sseFixtures {
		t.Run(fixture.file, func(t *testing.T) {
			t.Parallel()
			assertFixtureState(t, fixture, replayStream(t, false, true, readFixture(t, fixture.file)))
		})
	}
}

// Test flow:
//  1. For each recorded SSE fixture file, read its raw bytes and split them into randomly sized chunks (1-64 bytes, seeded per fixture).
//  2. Replay the chunk sequence through `replayStream`.
//  3. Assert the resulting state matches the same fixture expectations as the whole-blob replay, proving chunk boundaries do not change what is classified.
func TestFixtureStreamsClassifyIdenticallyUnderRandomChunking(t *testing.T) {
	t.Parallel()
	for fixtureIndex, fixture := range sseFixtures {
		t.Run(fixture.file, func(t *testing.T) {
			t.Parallel()
			body := readFixture(t, fixture.file)
			randomness := rand.New(rand.NewPCG(42, uint64(fixtureIndex)))
			chunks := make([][]byte, 0, len(body))
			for start := 0; start < len(body); {
				end := min(start+1+randomness.IntN(64), len(body))
				chunks = append(chunks, body[start:end])
				start = end
			}

			assertFixtureState(t, fixture, replayStream(t, false, true, chunks...))
		})
	}
}

// Test flow:
//  1. Read the newlineless_final_content.sse fixture, whose last event has no trailing newline.
//  2. Feed its whole body into the classifier's Classify and record the facts, without flushing yet.
//  3. Assert contentChunks is still 0, since the unflushed final event carries nothing.
//  4. Run state.classify, which flushes the classifier's tail.
//  5. Assert the terminal becomes TerminalLost and contentSource becomes delta.content once the tail is flushed.
func TestNewlineLessFinalEventLooksEmptyUntilTheStreamCloses(t *testing.T) {
	t.Parallel()
	body := readFixture(t, "newlineless_final_content.sse")
	classifier := newTestClassifier(false, nil)
	defer classifier.Release()

	state := &attemptState{receiptTime: testEpoch}
	state.record(classifier.Classify(body))

	if state.contentChunks != 0 {
		t.Fatalf("contentChunks = %d, want 0: an unflushed final event carries nothing yet", state.contentChunks)
	}

	state.classify(context.Background(), AttemptSpec{Model: qwenModel, Classifier: classifier}, nil)

	if state.terminal != TerminalLost {
		t.Errorf("terminal = %v, want TerminalLost from the flushed tail", state.terminal)
	}
	if state.contentSource != "delta.content" {
		t.Errorf("contentSource = %q, want %q", state.contentSource, "delta.content")
	}
}

// Test flow:
//  1. Build a table of garbage and truncated SSE bodies: empty input, a bare "data:" prefix, an open brace or bracket, a truncated tool_calls array, NUL bytes, wrongly typed choices/usage/tool_calls fields, a quoted-string body, a CRLF-only [DONE], and a truncated event followed by [DONE].
//  2. Replay each body through `replayStream`.
//  3. Assert no panic occurs, contentSource and errorSource stay empty, and the terminal is TerminalEmptyStream.
func TestGarbageAndTruncatedInputClassifyWithoutPanicking(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "single_newline", body: "\n"},
		{name: "only_prefix", body: "data:"},
		{name: "prefix_with_open_brace", body: "data: {"},
		{name: "prefix_with_open_bracket", body: `data: {"choices":[`},
		{name: "truncated_tool_calls", body: `data: {"choices":[{"delta":{"tool_calls":[`},
		{name: "nul_bytes", body: "data: \x00\x00\x00\n\n"},
		{name: "choices_not_an_array", body: `data: {"choices":{"delta":{"content":"hi"}}}` + "\n\n"},
		{name: "usage_not_an_object", body: `data: {"usage":7}` + "\n\n"},
		{name: "tool_calls_not_an_array", body: `data: {"choices":[{"delta":{"tool_calls":{"id":"x"}}}]}` + "\n\n"},
		{name: "deeply_quoted", body: `data: "just a string"` + "\n\n"},
		{name: "crlf_only", body: "data: [DONE]\r\n\r\n"},
		{name: "half_an_event_then_done", body: `data: {"choices":[{"delta"` + "\ndata: [DONE]\n\n"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			state := replayStream(t, false, true, []byte(testCase.body))

			if state.contentSource != "" {
				t.Errorf("contentSource = %q, want empty", state.contentSource)
			}
			if state.errorSource != "" {
				t.Errorf("errorSource = %q, want empty", state.errorSource)
			}
			if state.terminal != TerminalEmptyStream {
				t.Errorf("terminal = %v, want TerminalEmptyStream", state.terminal)
			}
		})
	}
}

// Test flow:
//  1. Build one SSE chunk whose content field holds invalid UTF-8 bytes.
//  2. Run `contentSource` on it.
//  3. Assert it still reports "delta.content" with ok true.
func TestInvalidUTF8ContentStillCountsAsContent(t *testing.T) {
	t.Parallel()
	source, ok := contentSource([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"\xff\xfe\"}}]}\n\n"), false)

	if !ok || source != "delta.content" {
		t.Errorf("contentSource = %q/%v, want delta.content/true", source, ok)
	}
}

// Test flow:
//  1. Build one SSE chunk carrying content and completion-token usage, and keep a clone of its original bytes.
//  2. Run `classifyChunk` on the chunk.
//  3. Assert the chunk's bytes are unchanged after classification.
func TestClassifyIsPureOverItsInput(t *testing.T) {
	t.Parallel()
	body := []byte(`data: {"choices":[{"delta":{"content":"hi"}}],"usage":{"completion_tokens":5}}` + "\n\n")
	original := bytes.Clone(body)

	classifyChunk(body, true)

	if !bytes.Equal(body, original) {
		t.Errorf("classifyChunk mutated its input: %q", body)
	}
}

// Test flow:
//  1. Build a table of 404 dispatch errors: one whose body names an unsupported protocol version, one whose body reports a torn-down escrow.
//  2. Run `state.classify` with each error as the dispatch failure.
//  3. Assert the terminal is TerminalNotFound in both cases.
//  4. Assert capability.VersionUnsupported is true only for the version-refusal body.
func TestClassifyCarriesAVersionRefusalOffTheDispatchError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want bool
	}{
		{name: "the host's build is too old", body: `version "v3" not found`, want: true},
		{name: "an escrow torn down mid-flight", body: `{"message":"get escrow: escrow not found"}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			state := attemptState{}
			refusal := &transport.UpstreamStatusError{
				Path: "/v1/chat/completions", StatusCode: http.StatusNotFound, Body: testCase.body,
			}

			state.classify(context.Background(), AttemptSpec{Classifier: &fakeClassifier{}}, refusal)

			if state.terminal != TerminalNotFound {
				t.Fatalf("terminal = %v, want %v", state.terminal, TerminalNotFound)
			}
			if got := state.capability.VersionUnsupported; got != testCase.want {
				t.Fatalf("capability.VersionUnsupported = %v, want %v", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build a table of SSE chunk bodies covering logprobs where every token and alternative is an id, and ones where the first token, a later token, an alternative, or a later event names its decoded text; plus a chunk with no logprobs at all.
//  2. Run `logprobsDecoded` on each case's body.
//  3. Assert the result matches the case's wantDecoded.
func TestLogprobsDecoded(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		body        string
		wantDecoded bool
	}{
		{
			name: "every token and alternative names an id",
			body: `data: {"choices":[{"logprobs":{"content":[{"token":"758","top_logprobs":[{"token":"12"}]},{"token":"91"}]}}]}` + "\n\n",
		},
		{
			name:        "the first token names its text",
			body:        `data: {"choices":[{"logprobs":{"content":[{"token":"The"}]}}]}` + "\n\n",
			wantDecoded: true,
		},
		{
			name:        "a later token names its text",
			body:        `data: {"choices":[{"logprobs":{"content":[{"token":"758"},{"token":"The"}]}}]}` + "\n\n",
			wantDecoded: true,
		},
		{
			name:        "an alternative names its text",
			body:        `data: {"choices":[{"logprobs":{"content":[{"token":"758","top_logprobs":[{"token":"12"},{"token":"The"}]}]}}]}` + "\n\n",
			wantDecoded: true,
		},
		{
			name:        "a later event names its text",
			body:        `data: {"choices":[{"logprobs":{"content":[{"token":"758"}]}}]}` + "\n\n" + `data: {"choices":[{"logprobs":{"content":[{"token":"The"}]}}]}` + "\n\n",
			wantDecoded: true,
		},
		{name: "no logprobs at all", body: `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := logprobsDecoded([]byte(testCase.body)); got != testCase.wantDecoded {
				t.Errorf("logprobsDecoded() = %v, want %v", got, testCase.wantDecoded)
			}
		})
	}
}

// Test flow:
//  1. Build a table of SSE chunk bodies where one sub-field is typed against the schema (logprobs as a list, a logprob token as a number, a completion-token count as a string), each still carrying valid delta content.
//  2. Run `scanChunk` on each case's body.
//  3. Assert ContentSource, UsageCompletionTokens, and LogprobsDecoded match the case's expectations, showing the mistyped field costs only its own value.
func TestAWrongTypedFieldOnlyCostsItsOwnAnswer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		body        string
		wantSource  string
		wantTokens  int64
		wantDecoded bool
	}{
		{
			name:       "logprobs sent as a list",
			body:       `data: {"choices":[{"delta":{"content":"hi"},"logprobs":[]}],"usage":{"completion_tokens":40}}` + "\n\n",
			wantSource: "delta.content", wantTokens: 40,
		},
		{
			name:       "a logprob token sent as a number",
			body:       `data: {"choices":[{"delta":{"content":"hi"},"logprobs":{"content":[{"token":758}]}}],"usage":{"completion_tokens":40}}` + "\n\n",
			wantSource: "delta.content", wantTokens: 40,
		},
		{
			name:        "a token count sent as a string",
			body:        `data: {"choices":[{"delta":{"content":"hi"},"logprobs":{"content":[{"token":"The"}]}}],"usage":{"completion_tokens":"40"}}` + "\n\n",
			wantSource:  "delta.content",
			wantDecoded: true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			scan := scanChunk([]byte(testCase.body), false)
			if scan.ContentSource != testCase.wantSource {
				t.Errorf("ContentSource = %q, want %q", scan.ContentSource, testCase.wantSource)
			}
			if scan.UsageCompletionTokens != testCase.wantTokens {
				t.Errorf("UsageCompletionTokens = %d, want %d", scan.UsageCompletionTokens, testCase.wantTokens)
			}
			if scan.LogprobsDecoded != testCase.wantDecoded {
				t.Errorf("LogprobsDecoded = %v, want %v", scan.LogprobsDecoded, testCase.wantDecoded)
			}
		})
	}
}

// The three questions the scan answers, each named for what its table drives.
func contentSource(events []byte, emptyAnswerCountsTokens bool) (string, bool) {
	source := scanChunk(events, emptyAnswerCountsTokens).ContentSource
	return source, source != ""
}

func usageCompletionTokens(events []byte) (int64, bool) {
	tokens := scanChunk(events, false).UsageCompletionTokens
	return tokens, tokens > 0
}

func logprobsDecoded(events []byte) bool {
	return scanChunk(events, false).LogprobsDecoded
}
