package engine

import (
	"fmt"
	"testing"
)

type contextLimitCall struct {
	participant string
	maxTokens   uint64
}

type stubCapabilityRecorder struct {
	contextLimits       []contextLimitCall
	toolsUnsupported    []string
	versionsUnsupported []string
}

func (r *stubCapabilityRecorder) RecordContextLimit(participant, model string, maxTokens uint64) {
	r.contextLimits = append(r.contextLimits, contextLimitCall{participant: participant, maxTokens: maxTokens})
}

func (r *stubCapabilityRecorder) RecordToolUnsupported(participant, model string) {
	r.toolsUnsupported = append(r.toolsUnsupported, participant)
}

func (r *stubCapabilityRecorder) RecordVersionUnsupported(participant string) {
	r.versionsUnsupported = append(r.versionsUnsupported, participant)
}

const (
	vllmContextLengthMessage = "This model's maximum context length is 131072 tokens. However, you requested 140000 tokens (12000 in the messages, 128000 in the completion). Please reduce the length of the messages or completion."
	vllmContextTotalMessage  = "This model's maximum context length is 40960 tokens. However, you requested 40000 output tokens and your prompt contains 1200 input tokens, for a total of 41200 tokens. Please reduce the length of the input prompt or the number of requested output tokens."
	vllmToolChoiceMessage    = "tool choice requires --enable-auto-tool-choice and --tool-call-parser to be set"

	malformedToolCallMessage   = "tool call arguments are not valid JSON: Expecting ',' delimiter: line 1 column 12 (char 11)"
	malformedToolCallRejection = `{"error":{"code":400,"message":"` + malformedToolCallMessage + `","type":"BadRequestError"}}`
)

// Test flow:
//  1. For each table case's error message (the older vLLM phrasing, the current one with its total, a tool-choice refusal, a rune that shrinks when lowercased, the limit at the end of the message, a case-insensitive phrase, lookalikes without a number, an unrelated error, an empty message, and an overflowing number), call `ParseCapabilityError`.
//  2. Assert the parsed `CapabilitySignal` matches the case's expectation.
func TestParseCapabilityError(t *testing.T) {
	testCases := []struct {
		name    string
		message string
		want    CapabilitySignal
	}{
		{
			name:    "maximum_context_length",
			message: vllmContextLengthMessage,
			want:    CapabilitySignal{ContextLimit: 131072, ContextRequested: 140000},
		},
		{
			name:    "total_of_tokens",
			message: vllmContextTotalMessage,
			want:    CapabilitySignal{ContextLimit: 40960, ContextRequested: 41200},
		},
		{
			name:    "tool_choice_unsupported",
			message: vllmToolChoiceMessage,
			want:    CapabilitySignal{ToolsUnsupported: true},
		},
		{
			name:    "a_rune_that_shrinks_when_lowercased_does_not_shift_the_digits",
			message: "Kontext: maximum context length is 131072 tokens",
			want:    CapabilitySignal{ContextLimit: 131072},
		},
		{
			name:    "limit_at_end_of_message",
			message: "This model's maximum context length is 8192",
			want:    CapabilitySignal{ContextLimit: 8192},
		},
		{
			name:    "case_insensitive_phrase",
			message: "Maximum Context Length Is 4096 tokens",
			want:    CapabilitySignal{ContextLimit: 4096},
		},
		{
			name:    "lookalike_without_a_number",
			message: "This model's maximum context length is exceeded by your request",
			want:    CapabilitySignal{},
		},
		{
			name:    "lookalike_total_without_a_number",
			message: "you requested too much, for a total of at least several thousand tokens",
			want:    CapabilitySignal{},
		},
		{
			name:    "unrelated_error",
			message: "the model is currently loading",
			want:    CapabilitySignal{},
		},
		{
			name:    "empty_message",
			message: "",
			want:    CapabilitySignal{},
		},
		{
			name:    "overflowing_number",
			message: "maximum context length is 99999999999999999999999 tokens",
			want:    CapabilitySignal{},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := ParseCapabilityError(testCase.message); got != testCase.want {
				t.Fatalf("signal = %+v, want %+v", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. For each table case's `CapabilitySignal` (context limit, tools unsupported, version unsupported, nothing parsed), call `Refused()` and `Retriable()`.
//  2. Assert a context-limit signal is refused but not retriable, tool/version-unsupported signals are both refused and retriable, and an empty signal is neither.
func TestCapabilitySignalTellsARetriableRefusalFromAContextLengthRejection(t *testing.T) {
	testCases := []struct {
		name          string
		signal        CapabilitySignal
		wantRefused   bool
		wantRetriable bool
	}{
		{name: "context_limit", signal: CapabilitySignal{ContextLimit: 8192}, wantRefused: true},
		{name: "tools_unsupported", signal: CapabilitySignal{ToolsUnsupported: true}, wantRefused: true, wantRetriable: true},
		{name: "version_unsupported", signal: CapabilitySignal{VersionUnsupported: true}, wantRefused: true, wantRetriable: true},
		{name: "nothing_parsed", signal: CapabilitySignal{}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := testCase.signal.Refused(); got != testCase.wantRefused {
				t.Fatalf("refused = %v, want %v", got, testCase.wantRefused)
			}
			if got := testCase.signal.Retriable(); got != testCase.wantRetriable {
				t.Fatalf("retriable = %v, want %v", got, testCase.wantRetriable)
			}
		})
	}
}

// Test flow:
//  1. Record a parsed context-limit capability and assert it lands in `contextLimits` with the right participant and token value, with nothing recorded as tools-unsupported.
//  2. Record a parsed tool-choice capability and assert it lands in `toolsUnsupported`, with no context limit recorded.
//  3. Record a lookalike phrase that parses to nothing and assert nothing is recorded.
//  4. Record a parsed context-limit capability against an unnamed participant and assert nothing is recorded.
func TestRecordCapability(t *testing.T) {
	t.Run("context_limit_is_recorded_with_its_parsed_value", func(t *testing.T) {
		t.Parallel()
		recorder := &stubCapabilityRecorder{}

		RecordCapability(recorder, testParticipant, "model-a", ParseCapabilityError(vllmContextLengthMessage))

		if len(recorder.contextLimits) != 1 {
			t.Fatalf("context limits = %+v, want one", recorder.contextLimits)
		}
		if recorder.contextLimits[0] != (contextLimitCall{participant: testParticipant, maxTokens: 131072}) {
			t.Fatalf("context limit = %+v, want 131072 for %s", recorder.contextLimits[0], testParticipant)
		}
		if len(recorder.toolsUnsupported) != 0 {
			t.Fatalf("tools unsupported = %+v, want none", recorder.toolsUnsupported)
		}
	})

	t.Run("tool_choice_is_recorded_without_a_context_limit", func(t *testing.T) {
		t.Parallel()
		recorder := &stubCapabilityRecorder{}

		RecordCapability(recorder, testParticipant, "model-a", ParseCapabilityError(vllmToolChoiceMessage))

		if len(recorder.toolsUnsupported) != 1 || recorder.toolsUnsupported[0] != testParticipant {
			t.Fatalf("tools unsupported = %+v, want %s", recorder.toolsUnsupported, testParticipant)
		}
		if len(recorder.contextLimits) != 0 {
			t.Fatalf("context limits = %+v, want none", recorder.contextLimits)
		}
	})

	t.Run("a_lookalike_phrase_records_nothing", func(t *testing.T) {
		t.Parallel()
		recorder := &stubCapabilityRecorder{}

		RecordCapability(recorder, testParticipant, "model-a", ParseCapabilityError("this model's maximum context length is exceeded"))

		if len(recorder.contextLimits) != 0 || len(recorder.toolsUnsupported) != 0 {
			t.Fatalf("recorder = %+v, want nothing recorded", recorder)
		}
	})

	t.Run("an_unnamed_participant_records_nothing", func(t *testing.T) {
		t.Parallel()
		recorder := &stubCapabilityRecorder{}

		RecordCapability(recorder, "", "model-a", ParseCapabilityError(vllmContextLengthMessage))

		if len(recorder.contextLimits) != 0 {
			t.Fatalf("context limits = %+v, want none", recorder.contextLimits)
		}
	})
}

// Test flow:
//  1. Build an attempt whose error stream carries a context-length refusal, and a copy with its error source cleared.
//  2. Call `CapabilityOf` on each.
//  3. Assert the error-stream attempt yields the parsed context limit, and the attempt with no error source yields an empty signal.
func TestCapabilityOfAttempt(t *testing.T) {
	errorStream := failedAttempt(TerminalCapabilityRefused)
	errorStream.ErrorSource = "sse_error"
	errorStream.ErrorMessage = vllmContextLengthMessage

	noErrorSource := errorStream
	noErrorSource.ErrorSource = ""

	testCases := []struct {
		name    string
		attempt AttemptOutcome
		want    CapabilitySignal
	}{
		{name: "error_stream_attempt", attempt: errorStream, want: CapabilitySignal{ContextLimit: 131072, ContextRequested: 140000}},
		{name: "attempt_without_an_error_event_is_ignored", attempt: noErrorSource},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := CapabilityOf(testCase.attempt); got != testCase.want {
				t.Fatalf("signal = %+v, want %+v", got, testCase.want)
			}
		})
	}
}

// errorEventAttempt builds an attempt whose host answered with this one error event, as the engine's own classifier reads it.
func errorEventAttempt(t *testing.T, payload string) AttemptOutcome {
	t.Helper()
	event := classifyChunk([]byte("data: "+payload+"\n\n"), false).Error
	if !event.present() {
		t.Fatalf("the classifier reads no error event in %s", payload)
	}
	return AttemptOutcome{
		ErrorSource:  event.Source,
		ErrorCode:    event.Code,
		ErrorType:    event.Type,
		ErrorMessage: event.Message,
		ErrorPayload: event.Payload,
	}
}

// Test flow:
//  1. For each table case's error payload, suspicion flag and content source, build an attempt via `errorEventAttempt` and set those fields.
//  2. Call `rulesOutRetry` on it.
//  3. Assert it rules out a retry only for a trusted, class-recognized error body from a non-suspicious host with no content already streamed.
func TestRulesOutRetryOnlyForATrustedAnswerEveryHostWouldRepeat(t *testing.T) {
	const (
		contextLengthRejection = `{"error":{"code":400,"message":"` + vllmContextTotalMessage + `","type":"BadRequestError"}}`
		toolChoiceRefusal      = `{"error":{"code":400,"message":"` + vllmToolChoiceMessage + `","type":"BadRequestError"}}`
		templateOrderMessage   = "Conversation roles must alternate user/assistant/user/assistant/..."
	)
	testCases := []struct {
		name          string
		payload       string
		suspicious    bool
		contentSource string
		want          bool
	}{
		{name: "trusted_context_length", payload: contextLengthRejection, want: true},
		{name: "trusted_malformed_tool_call_json_with_code_400", payload: `{"error":{"code":400,"message":"` + malformedToolCallMessage + `"}}`, want: true},
		{name: "trusted_bad_request_class_without_a_code", payload: `{"error":{"message":"` + templateOrderMessage + `","type":"BadRequestError"}}`, want: true},
		{name: "the_same_400_from_a_suspicious_host", payload: malformedToolCallRejection, suspicious: true},
		{name: "tool_choice_unsupported_refusal", payload: toolChoiceRefusal},
		{name: "code_404", payload: `{"error":{"code":404,"message":"the model kimi is not served here","type":"NotFoundError"}}`},
		{name: "code_422", payload: `{"error":{"code":422,"message":"` + malformedToolCallMessage + `","type":"UnprocessableEntityError"}}`},
		{name: "server_error_class", payload: `{"error":{"message":"backend failed","type":"server_error"}}`},
		{name: "empty_message", payload: `{"error":{"code":400,"message":"","type":"BadRequestError"}}`},
		{name: "content_streamed_before_the_error", payload: malformedToolCallRejection, contentSource: sourceDeltaContent},
		{name: "message_only_error_without_a_code_or_class", payload: `{"error":{"message":"` + malformedToolCallMessage + `"}}`},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			attempt := errorEventAttempt(t, testCase.payload)
			attempt.Suspicious = testCase.suspicious
			attempt.ContentSource = testCase.contentSource

			if got := rulesOutRetry(attempt, 0, noTokenEstimate); got != testCase.want {
				t.Fatalf("rulesOutRetry = %v, want %v", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. For each table case's model context length and a trusted host's context-length rejection (its own limit and the total it was asked for), build an attempt via `errorEventAttempt`.
//  2. Call `rulesOutRetry` with the model's context length.
//  3. Assert the rejection rules out a retry unless the model is known to run longer than the rejecting host and the request fits the model.
func TestAContextLengthRejectionRulesOutARetryOnlyWhenNoHostCouldServeTheRequest(t *testing.T) {
	testCases := []struct {
		name               string
		modelContextLength uint64
		message            string
		want               bool
	}{
		{name: "model_context_length_unknown", message: contextRejection(180000, 200000), want: true},
		{name: "host_runs_shorter_than_the_model_and_the_request_fits_the_model", modelContextLength: 400000, message: contextRejection(180000, 200000)},
		{name: "host_already_runs_the_whole_model_context", modelContextLength: 400000, message: contextRejection(400000, 450000), want: true},
		{name: "request_exceeds_the_model_context", modelContextLength: 400000, message: contextRejection(180000, 450000), want: true},
		{name: "older_phrasing_request_fits_the_model", modelContextLength: 400000, message: vllmContextLengthMessage},
		{name: "older_phrasing_request_exceeds_the_model", modelContextLength: 400000, message: "This model's maximum context length is 131072 tokens. However, you requested 500000 tokens (490000 in the messages, 10000 in the completion). Please reduce the length of the messages or completion.", want: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			attempt := errorEventAttempt(t, `{"error":{"code":400,"message":"`+testCase.message+`","type":"BadRequestError"}}`)

			if got := rulesOutRetry(attempt, testCase.modelContextLength, noTokenEstimate); got != testCase.want {
				t.Fatalf("rulesOutRetry = %v, want %v", got, testCase.want)
			}
		})
	}
}

func contextRejection(hostLimit, requested uint64) string {
	const outputTokens = 1000
	return fmt.Sprintf("This model's maximum context length is %d tokens. However, you requested %d output tokens and your prompt contains %d input tokens, for a total of %d tokens. Please reduce the length of the input prompt or the number of requested output tokens.", hostLimit, outputTokens, requested-outputTokens, requested)
}

// Test flow:
//  1. For each table case's dispatch error body (a version-mismatch message, an escrow-not-found message, a missing route, and other lookalikes), call `ParseVersionRefusal`.
//  2. Assert `VersionUnsupported` is true only for the version-mismatch and session-version-conflict bodies.
func TestOnlyAVersionRefusalIsReadAsAPermanentCapability(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want bool
	}{
		{name: "the host's build is too old", body: `version "v3" not found`, want: true},
		{name: "the refusal as the host sends it, newline and all", body: "version \"v3\" not found\n", want: true},
		{name: "an escrow torn down mid-flight", body: `{"message":"get escrow: escrow not found"}`},
		{name: "a plain missing route", body: `404 page not found`},
		{name: "a model that is not served", body: `{"message":"model not found"}`},
		{name: "some other quoted thing missing", body: `model "kimi" not found`},
		{name: "a quoted escrow missing", body: `escrow "49247" not found`},
		{name: "the word version alone", body: `unsupported version`},
		{name: "both halves present but describing different things", body: `version "v4" active; model "kimi" not found`},
		{name: "the escrow is bound to another version", body: `{"message":"session version conflict: stored v3, host v4"}`, want: true},
		{name: "the conflict as the host sends it, newline and all", body: "{\"message\":\"session version conflict: stored v3, host v4\"}\n", want: true},
		{name: "nothing at all", body: ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := ParseVersionRefusal(testCase.body).VersionUnsupported; got != testCase.want {
				t.Fatalf("ParseVersionRefusal(%q).VersionUnsupported = %v, want %v", testCase.body, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build an `AttemptOutcome` whose `Capability` field already carries a parsed version refusal.
//  2. Call `CapabilityOf` on it.
//  3. Assert the signal reports `VersionUnsupported` and is retriable.
//  4. Assert an attempt with no refusal at all reports none.
func TestAVersionRefusalReachesCapabilityOfThroughTheDispatchError(t *testing.T) {
	t.Parallel()
	refused := AttemptOutcome{Capability: ParseVersionRefusal(`version "v3" not found`)}

	signal := CapabilityOf(refused)

	if !signal.VersionUnsupported {
		t.Fatal("a version refusal carried on the dispatch error did not reach CapabilityOf")
	}
	if !signal.Retriable() {
		t.Fatal("a version refusal must let the race try another host")
	}
	if CapabilityOf(AttemptOutcome{}).Refused() {
		t.Fatal("an attempt with no refusal at all reported one")
	}
}

// Test flow:
//  1. Record a parsed version refusal against a participant.
//  2. Assert it lands only in `versionsUnsupported` for that participant, with nothing recorded as tools-unsupported or context-limited.
func TestAVersionRefusalIsRecordedAgainstTheParticipant(t *testing.T) {
	t.Parallel()
	recorder := &stubCapabilityRecorder{}

	RecordCapability(recorder, "host-0", "model-a", ParseVersionRefusal(`version "v3" not found`))

	if len(recorder.versionsUnsupported) != 1 || recorder.versionsUnsupported[0] != "host-0" {
		t.Fatalf("versions recorded = %v, want one for host-0", recorder.versionsUnsupported)
	}
	if len(recorder.toolsUnsupported) != 0 || len(recorder.contextLimits) != 0 {
		t.Error("a version refusal was recorded as some other capability")
	}
}

// Test flow:
//  1. Build a trusted host's context-length rejection that claims a limit as long as the model's and a request past it.
//  2. Ask whether it rules out a retry, once for a request the gateway's own estimate puts far below the model's length and once for one close to it.
//  3. Assert the claim stops the search only when the estimate makes it plausible: one host's word against the gateway's own count is that host's miss, not every host's answer.
func TestAContextLengthClaimTheEstimateContradictsKeepsTheSearchGoing(t *testing.T) {
	const modelContextLength = 400000
	testCases := []struct {
		name            string
		estimatedTokens uint64
		want            bool
	}{
		{name: "estimate_far_below_the_model", estimatedTokens: 2_000},
		{name: "estimate_close_to_the_model", estimatedTokens: 300_000, want: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			attempt := errorEventAttempt(t, `{"error":{"code":400,"message":"`+contextRejection(modelContextLength, 450000)+`","type":"BadRequestError"}}`)

			if got := rulesOutRetry(attempt, modelContextLength, testCase.estimatedTokens); got != testCase.want {
				t.Fatalf("rulesOutRetry = %v, want %v", got, testCase.want)
			}
		})
	}
}
