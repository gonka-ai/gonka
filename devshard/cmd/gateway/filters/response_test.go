package filters

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func readSSEFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sse", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return data
}

// rewriteWholeStream drives one StreamRewriter over the whole stream and returns everything it emits.
func rewriteWholeStream(t *testing.T, stream []byte) []byte {
	t.Helper()
	rewriter := NewStreamRewriter(LogprobIntent{}, true)
	emitted, err := rewriter.Write(stream)
	if err != nil {
		t.Fatalf("Write() = %v", err)
	}
	final, err := rewriter.Close()
	if err != nil {
		t.Fatalf("Close() = %v", err)
	}
	return append(emitted, final...)
}

// splitCompleteEvents splits an SSE stream into its "\n\n"-terminated events.
func splitCompleteEvents(t *testing.T, stream []byte) [][]byte {
	t.Helper()
	var events [][]byte
	for _, event := range bytes.SplitAfter(stream, []byte("\n\n")) {
		if len(event) == 0 {
			continue
		}
		events = append(events, event)
	}
	return events
}

// Test flow:
//  1. Build the expected list of client-stripped field names.
//  2. Assert clientStrippedFields equals that exact list.
func TestClientStrippedFieldsExactList(t *testing.T) {
	want := []string{
		"logprob",
		"logprobs",
		"top_logprobs",
		"token_ids",
		"prompt_token_ids",
		"prompt_logprobs",
	}
	if !reflect.DeepEqual(clientStrippedFields, want) {
		t.Errorf("clientStrippedFields = %#v, want %#v", clientStrippedFields, want)
	}
}

// Test flow:
//  1. For each fixture (`content_stream.sse`, `tool_calls_stream.sse`, `newlineless_final_content.sse`, `newlineless_final_error.sse`), read the SSE fixture.
//  2. Rewrite the whole stream through a StreamRewriter.
//  3. Assert the rewritten output is byte-for-byte identical to the input.
func TestStreamRewriterFixture_PureContentPassthrough(t *testing.T) {
	tests := []string{
		"content_stream.sse",
		"tool_calls_stream.sse",
		"newlineless_final_content.sse",
		"newlineless_final_error.sse",
	}
	for _, fixture := range tests {
		t.Run(fixture, func(t *testing.T) {
			input := readSSEFixture(t, fixture)
			got := rewriteWholeStream(t, input)
			if !bytes.Equal(got, input) {
				t.Errorf("rewritten stream = %q, want unchanged %q", got, input)
			}
		})
	}
}

// Test flow:
//  1. Rewrite a nil input and, separately, an empty byte slice through a StreamRewriter.
//  2. Assert both produce empty output.
func TestStreamRewriterFixture_EmptyAndNilInput(t *testing.T) {
	for _, name := range []string{"nil", "empty"} {
		t.Run(name, func(t *testing.T) {
			var input []byte
			if name == "empty" {
				input = []byte{}
			}
			got := rewriteWholeStream(t, input)
			if len(got) != 0 {
				t.Errorf("rewritten stream = %q, want empty", got)
			}
		})
	}
}

// Test flow:
//  1. Read the `logprobs_stream.sse` fixture and rewrite the whole stream.
//  2. Assert the rewritten output still ends with the unchanged "data: [DONE]\n\n" marker.
func TestStreamRewriterFixture_DoneMarkerPreservedExactly(t *testing.T) {
	input := readSSEFixture(t, "logprobs_stream.sse")
	got := rewriteWholeStream(t, input)
	if !bytes.HasSuffix(got, []byte("data: [DONE]\n\n")) {
		t.Errorf("rewritten stream does not end with an unchanged [DONE] event: %q", got)
	}
}

// Test flow:
//  1. Read the `logprobs_stream.sse` fixture and rewrite the whole stream.
//  2. Assert the output no longer contains "logprobs", "top_logprobs" or "logprob".
//  3. Assert the sibling content and finish_reason fields are still present.
func TestStreamRewriterFixture_StripsLogprobsFamily(t *testing.T) {
	input := readSSEFixture(t, "logprobs_stream.sse")
	got := rewriteWholeStream(t, input)
	for _, field := range []string{`"logprobs"`, `"top_logprobs"`, `"logprob"`} {
		if bytes.Contains(got, []byte(field)) {
			t.Errorf("rewritten stream output still contains %s: %q", field, got)
		}
	}
	if !bytes.Contains(got, []byte(`"content":"ok"`)) {
		t.Error("rewritten stream dropped sibling content field it must preserve")
	}
	if !bytes.Contains(got, []byte(`"finish_reason":"stop"`)) {
		t.Error("rewritten stream dropped sibling finish_reason field it must preserve")
	}
}

// Test flow:
//  1. Read the `token_ids_stream.sse` fixture and rewrite the whole stream.
//  2. Assert the output no longer contains "token_ids", "prompt_token_ids" or "prompt_logprobs".
//  3. Assert the sibling content field is still present.
func TestStreamRewriterFixture_StripsTokenIdFamily(t *testing.T) {
	input := readSSEFixture(t, "token_ids_stream.sse")
	got := rewriteWholeStream(t, input)
	for _, field := range []string{`"token_ids"`, `"prompt_token_ids"`, `"prompt_logprobs"`} {
		if bytes.Contains(got, []byte(field)) {
			t.Errorf("rewritten stream output still contains %s: %q", field, got)
		}
	}
	if !bytes.Contains(got, []byte(`"content":"ok"`)) {
		t.Error("rewritten stream dropped sibling content field it must preserve")
	}
}

// Test flow:
//  1. Read the `malformed_data_line.sse` fixture and rewrite the whole stream.
//  2. Assert the well-formed event's logprobs field was stripped.
//  3. Assert no "logprob" field or the malformed event's own content leaked into the output.
//  4. Assert the output still ends with the [DONE] marker.
func TestStreamRewriterFixture_MalformedEventIsDroppedNotForwarded(t *testing.T) {
	input := readSSEFixture(t, "malformed_data_line.sse")
	got := rewriteWholeStream(t, input)
	if bytes.Contains(got, []byte(`"token":"ok","logprob"`)) {
		t.Error("well-formed event's logprobs was not stripped")
	}
	if bytes.Contains(got, []byte("logprob")) {
		t.Errorf("malformed event leaked internal fields instead of being dropped: %q", got)
	}
	if bytes.Contains(got, []byte(`"broken":true`)) {
		t.Errorf("malformed event was forwarded: %q", got)
	}
	if !bytes.HasSuffix(got, []byte("data: [DONE]\n\n")) {
		t.Error("[DONE] marker not preserved after a malformed event")
	}
}

// Test flow:
//  1. Read the `comment_and_blank_lines.sse` fixture and rewrite the whole stream.
//  2. Assert the leading SSE comment line is preserved verbatim.
//  3. Assert the data event's logprobs field was stripped while its content field is preserved.
func TestStreamRewriterFixture_CommentAndBlankLinesPassThrough(t *testing.T) {
	input := readSSEFixture(t, "comment_and_blank_lines.sse")
	got := rewriteWholeStream(t, input)
	if !bytes.HasPrefix(got, []byte(": keep-alive\n\n")) {
		t.Errorf("comment line not preserved verbatim at head of output: %q", got)
	}
	if bytes.Contains(got, []byte(`"logprobs"`)) {
		t.Error("the data event's logprobs was not stripped")
	}
	if !bytes.Contains(got, []byte(`"content":"hi"`)) {
		t.Error("dropped sibling content field it must preserve")
	}
}

// Test flow:
//  1. For each fixture, rewrite the whole stream in one call to get a reference output.
//  2. Split the same fixture into complete SSE events and feed them one at a time to a fresh StreamRewriter, buffering everything it emits.
//  3. Assert the chunk-by-chunk result equals the whole-stream result.
func TestStreamRewriterFixture_ChunkByChunkMatchesWholeStream(t *testing.T) {
	fixtures := []string{
		"content_stream.sse",
		"tool_calls_stream.sse",
		"logprobs_stream.sse",
		"token_ids_stream.sse",
		"malformed_data_line.sse",
		"comment_and_blank_lines.sse",
	}
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			input := readSSEFixture(t, name)
			whole := rewriteWholeStream(t, input)
			rewriter := NewStreamRewriter(LogprobIntent{}, true)
			var chunked bytes.Buffer
			for _, event := range splitCompleteEvents(t, input) {
				emitted, err := rewriter.Write(event)
				if err != nil {
					t.Fatalf("Write() = %v", err)
				}
				chunked.Write(emitted)
			}
			final, err := rewriter.Close()
			if err != nil {
				t.Fatalf("Close() = %v", err)
			}
			chunked.Write(final)
			if !bytes.Equal(whole, chunked.Bytes()) {
				t.Errorf("chunk-by-chunk result differs from whole-stream result\n whole:   %q\n chunked: %q", whole, chunked.Bytes())
			}
		})
	}
}

// Test flow:
//  1. Read the `logprobs_stream.sse` fixture and split it into events, then cut the second event mid-way through its "top_logprobs" field.
//  2. Write the truncated fragment to a StreamRewriter and assert nothing is emitted yet.
//  3. Close the rewriter and assert it returns ErrStreamTruncatedEvent with nothing emitted, so a mid-event cutoff is dropped and reported rather than forwarded with its internal fields.
func TestStreamRewriterFixture_TruncatedEventIsDroppedAndReported(t *testing.T) {
	input := readSSEFixture(t, "logprobs_stream.sse")
	events := splitCompleteEvents(t, input)
	target := events[1]
	splitAt := bytes.Index(target, []byte("top_logprobs")) + len("top_logprobs")
	if splitAt <= len("top_logprobs") || splitAt >= len(target) {
		t.Fatalf("fixture no longer contains a mid-event top_logprobs split point")
	}

	rewriter := NewStreamRewriter(LogprobIntent{}, true)
	emitted, err := rewriter.Write(target[:splitAt])
	if err != nil {
		t.Fatalf("Write() = %v", err)
	}
	if len(emitted) != 0 {
		t.Errorf("truncated data event must be held, not emitted, got %q", emitted)
	}

	final, err := rewriter.Close()
	if !errors.Is(err, ErrStreamTruncatedEvent) {
		t.Errorf("Close() error = %v, want ErrStreamTruncatedEvent", err)
	}
	if len(final) != 0 {
		t.Errorf("truncated data event must be dropped, got %q", final)
	}
}

// Test flow:
//  1. Build a response body carrying logprobs, token_ids, prompt_token_ids and prompt_logprobs at different nesting depths.
//  2. Strip the body with an empty LogprobIntent.
//  3. Assert the result is valid JSON and contains none of the internal field names.
//  4. Assert finish_reason and message.content survive the strip.
func TestStripResponseBody_RemovesAllInternalFieldsAtAnyDepth(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl-full",
		"object": "chat.completion",
		"choices": [
			{
				"index": 0,
				"message": {"role": "assistant", "content": "hi"},
				"logprobs": {"content": [{"token": "hi", "logprob": -0.1, "top_logprobs": [{"token": "hi", "logprob": -0.1}]}]},
				"token_ids": [1, 2, 3],
				"finish_reason": "stop"
			}
		],
		"prompt_token_ids": [9, 8, 7],
		"prompt_logprobs": null
	}`)
	got := stripResponseBody(body, LogprobIntent{})
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("stripResponseBody(, LogprobIntent{}) produced invalid JSON: %v (%q)", err, got)
	}
	for _, field := range []string{"logprob", "logprobs", "top_logprobs", "token_ids", "prompt_token_ids", "prompt_logprobs"} {
		if bytes.Contains(got, fmt.Appendf(nil, "%q", field)) {
			t.Errorf("stripResponseBody(, LogprobIntent{}) output still contains %q: %s", field, got)
		}
	}
	choices, ok := decoded["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("choices missing or malformed after strip: %#v", decoded["choices"])
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		t.Fatalf("choices[0] is not an object: %#v", choices[0])
	}
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %#v, want %q", choice["finish_reason"], "stop")
	}
	message, ok := choice["message"].(map[string]any)
	if !ok || message["content"] != "hi" {
		t.Errorf("message.content not preserved: %#v", choice["message"])
	}
}

// Test flow:
//  1. Strip a response body that carries none of the internal fields.
//  2. Assert the output is byte-for-byte unchanged.
func TestStripResponseBody_NoChangeReturnsEquivalentBytes(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-plain","choices":[{"message":{"content":"hi"}}]}`)
	got := stripResponseBody(body, LogprobIntent{})
	if !bytes.Equal(got, body) {
		t.Errorf("stripResponseBody(, LogprobIntent{}) = %q, want unchanged %q", got, body)
	}
}

// Test flow:
//  1. Strip a body that is not valid JSON.
//  2. Assert the output passes through unchanged.
func TestStripResponseBody_MalformedBodyPassesThroughUnchanged(t *testing.T) {
	body := []byte(`this is not json`)
	got := stripResponseBody(body, LogprobIntent{})
	if !bytes.Equal(got, body) {
		t.Errorf("stripResponseBody(, LogprobIntent{}) = %q, want unchanged %q", got, body)
	}
}

// Test flow:
//  1. Strip an empty byte slice.
//  2. Assert the output stays empty.
func TestStripResponseBody_EmptyBodyPassesThroughUnchanged(t *testing.T) {
	got := stripResponseBody([]byte{}, LogprobIntent{})
	if len(got) != 0 {
		t.Errorf("stripResponseBody(, LogprobIntent{}) = %q, want empty", got)
	}
}

// Test flow:
//  1. Strip a body whose prompt_logprobs field is set to null.
//  2. Assert the decoded output no longer has a prompt_logprobs key at all.
func TestStripResponseBody_NullValuedFieldAlsoStripped(t *testing.T) {
	body := []byte(`{"id":"x","prompt_logprobs":null,"choices":[]}`)
	got := stripResponseBody(body, LogprobIntent{})
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}
	if _, exists := decoded["prompt_logprobs"]; exists {
		t.Errorf("prompt_logprobs key still present after strip: %s", got)
	}
}

// Test flow:
//  1. Call IsCacheableUpstreamError with a table of status codes and error bodies, varying between deterministic validation errors, transient/capability errors, malformed bodies and non-400 statuses.
//  2. Assert each case's result matches the expected cacheability.
func TestIsCacheableUpstreamError(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"deterministic validation error is cacheable", 400, `{"error":{"type":"invalid_request_error","message":"temperature must be between 0 and 2"}}`, true},
		{"legacy object-error shape is cacheable", 400, `{"object":"error","type":"invalid_request_error","message":"unknown parameter foo"}`, true},
		{"empty message is not cacheable", 400, `{"error":{"message":""}}`, false},
		{"body without a recognizable error shape is not cacheable", 400, `{"choices":[]}`, false},
		{"malformed body is not cacheable", 400, `not json`, false},
		{"null code does not crash and is cacheable", 400, `{"error":{"code":null,"message":"fine"}}`, true},
		{"status 200 is never treated as a cacheable error", 200, `{"error":{"message":"anything"}}`, false},
		{"status 500 is never cacheable regardless of message", 500, `{"error":{"message":"a fixed validation failure"}}`, false},
		{"status 429 is never cacheable", 429, `{"error":{"message":"a fixed validation failure"}}`, false},
		{"status 401 is never cacheable", 401, `{"error":{"message":"a fixed validation failure"}}`, false},
		{"tool choice capability error is not cacheable", 400, `{"error":{"message":"tool choice requires --enable-auto-tool-choice and --tool-call-parser to be set"}}`, false},
		{"context length capability error is not cacheable", 400, `{"error":{"message":"This model's maximum context length is 120000 tokens"}}`, false},
		{"context length marker with no digits is cacheable", 400, `{"error":{"message":"maximum context length is unknown here"}}`, true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := IsCacheableUpstreamError(testCase.status, []byte(testCase.body))
			if got != testCase.want {
				t.Errorf("IsCacheableUpstreamError(%d, %s) = %v, want %v", testCase.status, testCase.body, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Call IsCacheableUpstreamError(400, ...) with a table of error bodies that name a failure through a numeric code, a type/class string, or only a message, in the order the rule reads them: status, then class, then message.
//  2. Assert each case's cacheability matches whether the host named a request problem or a momentary one.
func TestAnErrorIsJudgedByWhatTheHostNamed(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "a numeric code of 400 is about the request", body: `{"object":"error","code":400,"message":"context too long","type":"BadRequestError"}`, want: true},
		{name: "422 likewise", body: `{"error":{"code":422,"message":"unprocessable"}}`, want: true},
		{name: "404 names a model another host may serve", body: `{"error":{"code":404,"message":"the model does not exist"}}`},
		{name: "429 is about the moment", body: `{"error":{"code":429,"message":"slow down"}}`},
		{name: "500 is about the moment", body: `{"error":{"code":500,"message":"a fixed validation failure"}}`},
		{name: "a status wins over an innocent message", body: `{"error":{"code":503,"message":"everything is fine"}}`},
		{name: "a class the host named, with no status", body: `{"object":"error","code":null,"message":"boom","type":"server_error"}`},
		{name: "a request class, with no status", body: `{"error":{"type":"BadRequestError","message":"unknown parameter foo"}}`, want: true},
		{name: "a class spelled in the code", body: `{"error":{"code":"rate_limit_exceeded","message":"generic failure"}}`},
		{name: "nothing but a message, which is all the host gave", body: `{"error":{"message":"upstream request timed out"}}`},
		{name: "nothing but a message that names no failure", body: `{"error":{"message":"temperature must be between 0 and 2"}}`, want: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := IsCacheableUpstreamError(400, []byte(testCase.body)); got != testCase.want {
				t.Errorf("IsCacheableUpstreamError(400, %s) = %v, want %v", testCase.body, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. For every marker in momentaryFailureMessages, build three error bodies that place the marker in the message, in the type field (uppercased), and in the code field.
//  2. Assert IsCacheableUpstreamError(400, ...) is false for all three placements.
func TestIsCacheableUpstreamError_EveryMarkerExcludes(t *testing.T) {
	for _, marker := range momentaryFailureMessages {
		t.Run("marker in message: "+marker, func(t *testing.T) {
			body := fmt.Sprintf(`{"error":{"message":%q}}`, "request failed: "+marker+" occurred")
			if got := IsCacheableUpstreamError(400, []byte(body)); got {
				t.Errorf("IsCacheableUpstreamError() = true for message marker %q, want false", marker)
			}
		})
		t.Run("marker in type, case-insensitive: "+marker, func(t *testing.T) {
			body := fmt.Sprintf(`{"error":{"type":%q,"message":"generic failure"}}`, strings.ToUpper(marker))
			if got := IsCacheableUpstreamError(400, []byte(body)); got {
				t.Errorf("IsCacheableUpstreamError() = true for type marker %q, want false", marker)
			}
		})
		t.Run("marker in code: "+marker, func(t *testing.T) {
			body := fmt.Sprintf(`{"error":{"code":%q,"message":"generic failure"}}`, marker)
			if got := IsCacheableUpstreamError(400, []byte(body)); got {
				t.Errorf("IsCacheableUpstreamError() = true for code marker %q, want false", marker)
			}
		})
	}
}

// Test flow:
//  1. Call IsCacheableResponse with a table of status/body pairs covering plain successes, a 204, an empty body, a completed SSE stream, SSE-embedded transient and deterministic errors under both success and error statuses, a 500, and CRLF-framed SSE.
//  2. Assert each case's result matches the expected cacheability.
func TestIsCacheableResponseCoversSuccessesAndSSEEmbeddedFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"a plain success is cacheable", 200, `{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}]}`, true},
		{"a 204 is cacheable", 204, `{"choices":[]}`, true},
		{"an empty body is never cacheable", 200, ``, false},
		{"a completed sse stream is cacheable", 200, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", true},
		{"a success carrying a transient error inside an sse event is not cacheable", 200, "data: {\"choices\":[]}\n\ndata: {\"error\":{\"message\":\"upstream timeout\"}}\n\n", false},
		{"a success carrying a deterministic error inside an sse event is still not a success", 200, "data: {\"error\":{\"message\":\"temperature must be between 0 and 2\"}}\n\n", false},
		{"a 400 carrying a deterministic error inside an sse event is cacheable", 400, "data: {\"error\":{\"type\":\"invalid_request_error\",\"message\":\"unknown parameter foo\"}}\n\n", true},
		{"a 400 carrying a transient error inside an sse event is not cacheable", 400, "data: {\"error\":{\"message\":\"service unavailable\"}}\n\n", false},
		{"a 500 is not cacheable", 500, `{"choices":[]}`, false},
		{"crlf sse framing is scanned too", 200, "data: {\"error\":{\"message\":\"rate limit exceeded\"}}\r\n\r\n", false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := IsCacheableResponse(testCase.status, []byte(testCase.body)); got != testCase.want {
				t.Errorf("IsCacheableResponse(%d, %q) = %v, want %v", testCase.status, testCase.body, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Call HasNonCacheableError with a table of bodies covering a clean completion, plain and SSE-embedded transient/deterministic errors, a malformed body, a failure hidden behind wrongly typed choices, and a stream with two errors.
//  2. Assert each case's result matches whether a momentary failure is present, and that when two errors appear only the first is considered.
func TestHasNonCacheableErrorFindsFailuresRegardlessOfFraming(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"a clean completion carries none", `{"choices":[{"message":{"content":"hi"}}]}`, false},
		{"a plain transient error", `{"error":{"message":"upstream request timeout"}}`, true},
		{"a plain deterministic error is replayable", `{"error":{"message":"temperature must be between 0 and 2"}}`, false},
		{"an sse-embedded transient error", "data: {\"choices\":[]}\n\ndata: {\"error\":{\"message\":\"overloaded\"}}\n\n", true},
		{"an sse stream with no error", "data: {\"choices\":[]}\n\ndata: [DONE]\n\n", false},
		{"a malformed body carries none", `not json`, false},
		{"a failure a host hid behind wrongly typed choices, framed", "data: {\"error\":{\"message\":\"service unavailable\"},\"choices\":\"none\"}\n\n", true},
		{"the first failure is the one kept", "data: {\"error\":{\"message\":\"temperature must be between 0 and 2\"}}\n\ndata: {\"error\":{\"message\":\"overloaded\"}}\n\n", false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := HasNonCacheableError([]byte(testCase.body)); got != testCase.want {
				t.Errorf("HasNonCacheableError(%q) = %v, want %v", testCase.body, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Strip a body carrying a seed integer too large to round-trip through float64, alongside a logprobs field.
//  2. Assert the seed value in the output is byte-for-byte the same as sent.
//  3. Assert logprobs was actually removed, so the seed check is not vacuous.
func TestStripKeepsIntegersTooLargeForFloat64(t *testing.T) {
	body := []byte(`{"id":"c","seed":9007199254740993,"logprobs":{"content":[]},"choices":[]}`)

	stripped := string(stripResponseBody(body, LogprobIntent{}))

	if !strings.Contains(stripped, `"seed":9007199254740993`) {
		t.Fatalf("seed was rewritten: %s", stripped)
	}
	if strings.Contains(stripped, "logprobs") {
		t.Fatalf("the strip did not run at all, so the seed was never at risk: %s", stripped)
	}
}

// Test flow:
//  1. Strip a body that has a second JSON value trailing after the first object.
//  2. Assert the output is byte-for-byte unchanged, the way json.Unmarshal treats a malformed body.
func TestStripLeavesABodyWithTrailingJunkAlone(t *testing.T) {
	body := []byte(`{"logprobs":{"content":[]}} {"and":"more"}`)

	if got := string(stripResponseBody(body, LogprobIntent{})); got != string(body) {
		t.Fatalf("a malformed body was rewritten:\n got %s\nwant %s", got, body)
	}
}

// Test flow:
//  1. Build a response body nested to depths of 1,000, 1,000,000 and 3,000,000 brackets.
//  2. Strip each body.
//  3. Assert stripping produces non-empty output for every depth, so the decoder's own depth bound catches the nesting instead of overflowing the stack.
func TestADeeplyNestedResponseCannotCrashTheProcess(t *testing.T) {
	for _, depth := range []int{1_000, 1_000_000, 3_000_000} {
		body := []byte(`{"logprobs":1,"a":` + strings.Repeat("[", depth) + strings.Repeat("]", depth) + `}`)

		if got := stripResponseBody(body, LogprobIntent{}); len(got) == 0 {
			t.Fatalf("depth %d produced nothing", depth)
		}
	}
}

// Test flow:
//  1. Strip a shallow body carrying logprobs and assert the field is removed.
//  2. Strip a body nested 100,000 levels deep, past the decoder's depth limit, also carrying logprobs.
//  3. Assert the deep body still contains logprobs, since past the limit the body is unparseable and passes through whole.
func TestPastTheDepthLimitTheStripIsBypassed(t *testing.T) {
	shallow := []byte(`{"logprobs":1,"a":[[]]}`)
	if strings.Contains(string(stripResponseBody(shallow, LogprobIntent{})), "logprobs") {
		t.Fatal("a shallow body kept its internal field")
	}

	deep := []byte(`{"logprobs":1,"a":` + strings.Repeat("[", 100_000) + strings.Repeat("]", 100_000) + `}`)

	if !strings.Contains(string(stripResponseBody(deep, LogprobIntent{})), "logprobs") {
		t.Fatal("the deep body was stripped after all -- then this test, and the bypass it documents, are stale")
	}
}

// Test flow:
//  1. Strip a body carrying a seed of 1e999, a number past float64 range, alongside a logprobs field.
//  2. Assert logprobs was removed, so one out-of-range number does not turn the strip off.
//  3. Assert the seed is passed through as "1e999" rather than rewritten.
func TestAnOutOfRangeNumberDoesNotDisableTheStrip(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"hi"},"logprobs":{"a":1}}],"seed":1e999}`)

	stripped := string(stripResponseBody(body, LogprobIntent{}))

	if strings.Contains(stripped, "logprobs") {
		t.Fatalf("one out-of-range number turned the strip off: %s", stripped)
	}
	if !strings.Contains(stripped, "1e999") {
		t.Fatalf("the number was rewritten rather than passed through: %s", stripped)
	}
}

// Test flow:
//  1. Strip a body whose message content contains "<div> a & b".
//  2. Assert the output still contains that content unescaped, rather than the encoder's default HTML-escaped form.
func TestGeneratedMarkupIsNotEscaped(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"<div> a & b"}}],"logprobs":{}}`)

	if got := string(stripResponseBody(body, LogprobIntent{})); !strings.Contains(got, "<div> a & b") {
		t.Fatalf("markup was escaped on the way out: %s", got)
	}
}

// Test flow:
//  1. Strip a response body carrying logprobs with alternatives, prompt_logprobs and token_ids, once for each LogprobIntent: asking for neither, logprobs only, and logprobs with alternatives.
//  2. Assert the logprob field's presence matches whether logprobs were asked for.
//  3. Assert the alternative token's presence matches whether alternatives were asked for, and that logprobs-without-alternatives still leaves an empty top_logprobs array.
//  4. Assert prompt_logprobs and token_ids never reach the client, since internals are never anyone's to ask for.
func TestTheStripFollowsWhatTheClientAskedFor(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"hi"},"logprobs":{"content":[{"token":"hi","logprob":-0.5,"top_logprobs":[{"token":"hello","logprob":-1.5}]}]}}],"prompt_logprobs":[1],"token_ids":[7]}`)

	testCases := []struct {
		name          string
		intent        LogprobIntent
		wantLogprobs  bool
		wantTopFilled bool
	}{
		{name: "asked_for_neither", intent: LogprobIntent{}},
		{name: "asked_for_logprobs_only", intent: LogprobIntent{Keep: true}, wantLogprobs: true},
		{name: "asked_for_alternatives_too", intent: LogprobIntent{Keep: true, KeepTop: true}, wantLogprobs: true, wantTopFilled: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			stripped := string(stripResponseBody(body, testCase.intent))

			if held := strings.Contains(stripped, `"logprob"`); held != testCase.wantLogprobs {
				t.Fatalf("logprob present = %v, want %v: %s", held, testCase.wantLogprobs, stripped)
			}
			if filled := strings.Contains(stripped, `"hello"`); filled != testCase.wantTopFilled {
				t.Fatalf("alternatives present = %v, want %v: %s", filled, testCase.wantTopFilled, stripped)
			}
			if testCase.wantLogprobs && !testCase.wantTopFilled && !strings.Contains(stripped, `"top_logprobs":[]`) {
				t.Fatalf("top_logprobs must stay present and empty, which is the shape a client without alternatives expects: %s", stripped)
			}
			for _, internal := range []string{"prompt_logprobs", "token_ids"} {
				if strings.Contains(stripped, internal) {
					t.Fatalf("%s reached the client: %s", internal, stripped)
				}
			}
		})
	}
}

// Test flow:
//  1. Normalize a request body for each case: no logprobs ask, logprobs alone, logprobs with top_logprobs, and top_logprobs without logprobs.
//  2. Assert the returned LogprobIntent matches what the client actually asked for.
//  3. Assert the upstream request body carries "logprobs":true only when the client's own intent asked to keep logprobs.
func TestTheLogprobIntentIsWhatTheClientWrote(t *testing.T) {
	testCases := []struct {
		name       string
		body       string
		wantIntent LogprobIntent
	}{
		{name: "asked_for_neither", body: `{"model":"qwen","messages":[{"role":"user","content":"hi"}]}`},
		{name: "asked_for_logprobs", body: `{"model":"qwen","messages":[{"role":"user","content":"hi"}],"logprobs":true}`, wantIntent: LogprobIntent{Keep: true}},
		{name: "asked_for_alternatives", body: `{"model":"qwen","messages":[{"role":"user","content":"hi"}],"logprobs":true,"top_logprobs":3}`, wantIntent: LogprobIntent{Keep: true, KeepTop: true}},
		{name: "alternatives_without_logprobs", body: `{"model":"qwen","messages":[{"role":"user","content":"hi"}],"top_logprobs":3}`},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result, err := NormalizeRequest([]byte(testCase.body), Options{RoutedModel: "qwen"})
			if err != nil {
				t.Fatalf("NormalizeRequest: %v", err)
			}
			if result.Logprobs != testCase.wantIntent {
				t.Fatalf("intent = %+v, want %+v", result.Logprobs, testCase.wantIntent)
			}
			if carries := strings.Contains(string(result.Body), `"logprobs":true`); carries != testCase.wantIntent.Keep {
				t.Fatalf("the request upstream asks for logprobs = %v, want the client's own %v: %s",
					carries, testCase.wantIntent.Keep, result.Body)
			}
		})
	}
}

// Test flow:
//  1. Assert every field in requestableFields also appears in clientStrippedFields.
//  2. For every field in clientStrippedFields, assert it belongs to exactly one of requestableFields or alwaysStrippedFields, never both or neither.
func TestTheStripSetsPartitionTheFullList(t *testing.T) {
	for _, field := range requestableFields {
		if !slices.Contains(clientStrippedFields, field) {
			t.Fatalf("%q is requestable but not in the strip list, so a client that asked for nothing still sees it", field)
		}
	}
	for _, field := range clientStrippedFields {
		requestable := slices.Contains(requestableFields, field)
		always := slices.Contains(alwaysStrippedFields, field)
		if requestable == always {
			t.Fatalf("%q is in %s, want exactly one of requestable and always-stripped",
				field, map[bool]string{true: "both sets", false: "neither set"}[requestable])
		}
	}
}

// Test flow:
//  1. For every field in clientStrippedFields, build a response body carrying that field on choices[0].
//  2. Strip the body.
//  3. Assert the field name no longer appears in the output.
func TestEveryStrippedFieldIsRemoved(t *testing.T) {
	for _, field := range clientStrippedFields {
		body := []byte(`{"choices":[{"index":0,"` + field + `":{"content":[]}}]}`)

		stripped := stripResponseBody(body, LogprobIntent{})

		if bytes.Contains(stripped, []byte(field)) {
			t.Fatalf("field %q survived the strip: %s", field, stripped)
		}
	}
}

// Test flow:
//  1. Strip a response body whose logprobs field carries a bareword -Infinity value, alongside token_ids and prompt_logprobs.
//  2. Assert token_ids, prompt_logprobs and logprob are all gone from the output.
//  3. Assert the client's content survives.
//  4. Assert the stripped output decodes as valid JSON.
func TestABodyCarryingNonFiniteNumbersIsStillStrippedAndDelivered(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"hi"},"logprobs":{"content":[{"logprob":-Infinity}]}}],"token_ids":[7],"prompt_logprobs":[1]}`)

	stripped := stripResponseBody(body, LogprobIntent{})

	for _, internal := range []string{"token_ids", "prompt_logprobs", "logprob"} {
		if bytes.Contains(stripped, []byte(internal)) {
			t.Fatalf("%s reached the client: %s", internal, stripped)
		}
	}
	if !bytes.Contains(stripped, []byte(`"content":"hi"`)) {
		t.Fatalf("the client's answer was lost: %s", stripped)
	}
	var decoded any
	if err := json.Unmarshal(stripped, &decoded); err != nil {
		t.Fatalf("the delivered body is not JSON: %v (%s)", err, stripped)
	}
}

// Test flow:
//  1. Assert the raw body fails to parse on its own, since it carries a bareword NaN, so this test proves something about what stripping changes.
//  2. Strip a body whose message content contains the words "NaN" and "-Infinity" as plain text, alongside a bareword NaN inside logprobs and a token_ids field.
//  3. Assert the content string is left unchanged.
//  4. Assert token_ids no longer appears in the output.
func TestNonFiniteWordsInsideStringsAreLeftAlone(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"the value is NaN, or -Infinity"},"logprobs":{"content":[{"logprob":NaN}]}}],"token_ids":[7]}`)
	var probe any
	if json.Unmarshal(body, &probe) == nil {
		t.Fatal("the body parses on its own, so normalisation never runs and this test is vacuous")
	}

	stripped := stripResponseBody(body, LogprobIntent{})

	if !bytes.Contains(stripped, []byte(`the value is NaN, or -Infinity`)) {
		t.Fatalf("content was rewritten: %s", stripped)
	}
	if bytes.Contains(stripped, []byte("token_ids")) {
		t.Fatalf("an internal field survived: %s", stripped)
	}
}

// Test flow:
//  1. Build an SSE payload with an empty {"error":{}} event followed by an event naming a real error message.
//  2. Parse the upstream error details from the payload.
//  3. Assert the scan found an error and reports the later event's message, so an empty error does not stop it.
func TestAnEmptyErrorEventDoesNotStopTheScan(t *testing.T) {
	payload := []byte("data: {\"error\":{}}\n\n" + "data: {\"object\":\"error\",\"message\":\"service unavailable\"}\n\n")

	details, found := parseUpstreamErrorDetails(payload)

	if !found {
		t.Fatal("the scan stopped at the empty error and never saw the real one")
	}
	if details.Message != "service unavailable" {
		t.Fatalf("message = %q, want the later event's", details.Message)
	}
}

// Test flow:
//  1. Call IsCacheableResponse(200, ...) with a table of stream and completion bodies, varying how a choice signals it finished: no finish_reason, a later event supplying it, a terminal reason not repeated later, null/empty finish_reason, multiple choices where only some finish, stop_reason alone, differently cased "Choices", split-across-lines events, error-only bodies, and malformed or cut-off bodies.
//  2. Assert each case's cacheability matches whether every choice the reply started was actually brought to an end.
func TestAnAnswerThatNeverFinishedIsNotCacheable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "a choice that never finished",
			body: "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"still working\"}}]}\n\ndata: [DONE]\n\n",
		},
		{
			name: "a choice finished by a later event",
			body: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
			want: true,
		},
		{
			name: "a terminal reason a later event does not name again",
			body: "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"trailing\"}}]}\n\ndata: [DONE]\n\n",
			want: true,
		},
		{
			name: "a null finish_reason is not terminal",
			body: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n",
		},
		{
			name: "an empty finish_reason is not terminal",
			body: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"\"}]}\n\ndata: [DONE]\n\n",
		},
		{
			name: "a second choice that never finished",
			body: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"},\"finish_reason\":\"stop\"},{\"index\":1,\"delta\":{\"content\":\"b\"}}]}\n\n",
		},
		{
			name: "an unindexed second choice is not vouched for by its sibling",
			body: "data: {\"choices\":[{\"delta\":{\"content\":\"a\"},\"finish_reason\":\"stop\"},{\"delta\":{\"content\":\"b\"}}]}\n\n",
		},
		{
			name: "the usage-only event a stream ends with starts no choice",
			body: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"completion_tokens\":7}}\n\ndata: [DONE]\n\n",
			want: true,
		},
		{
			name: "a completion terminated by stop_reason alone, as the chunk conversion reads it",
			body: `{"object":"chat.completion","choices":[{"index":0,"message":{"content":"ok"},"finish_reason":null,"stop_reason":128009}]}`,
			want: true,
		},
		{
			name: "a length cut is an ending like any other",
			body: "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n",
			want: true,
		},
		{
			name: "a choice a host spelled with a capital, still unfinished",
			body: `{"object":"chat.completion","Choices":[{"index":0,"message":{"content":"half"},"finish_reason":null}]}`,
		},
		{
			name: "a folded completion that never finished",
			body: `{"choices":[{"index":0,"message":{"content":"hi"},"finish_reason":null}]}`,
		},
		{
			name: "a folded completion that finished",
			body: `{"choices":[{"index":0,"message":{"content":"hi"},"finish_reason":"stop"}]}`,
			want: true,
		},
		{
			name: "an unframed completion a host sent instead of a stream",
			body: `{"object":"chat.completion","choices":[{"index":0,"message":{"content":"hi"}}]}`,
		},
		{
			name: "a choice split across two data lines is one object, as a client reads it",
			body: "data: {\"choices\":[{\"index\":0,\ndata: \"delta\":{\"content\":\"half\"}}]}\n\n",
		},
		{
			name: "a finished choice split across two data lines",
			body: "data: {\"choices\":[{\"index\":0,\ndata: \"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
			want: true,
		},
		{
			name: "a transient failure a host hid behind a choices field of the wrong type",
			body: `{"error":{"message":"service unavailable"},"choices":"none"}`,
		},
		{
			name: "an error object carrying nothing is not replayable either",
			body: `{"error":{}}`,
		},
		{
			name: "an event cut off mid-object",
			body: "data: {\"choices\":[{\"index\":0,\"delta\":{\"cont\n\n",
		},
		{
			name: "an index spelled as a string falls back to where the choice stands",
			body: `{"choices":[{"index":"0","message":{"content":"a"},"finish_reason":"stop"},{"index":"1","message":{"content":"b"}}]}`,
		},
		{
			name: "an index past what an int64 holds falls back the same way",
			body: `{"choices":[{"index":1e30,"message":{"content":"a"},"finish_reason":"stop"},{"index":1e30,"message":{"content":"b"}}]}`,
		},
		{
			name: "an empty error event is one event among many, and stops nothing",
			body: "data: {\"error\":{}}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
			want: true,
		},
		{
			name: "a body that is neither JSON nor events",
			body: `<html>upstream is down</html>`,
		},
		{
			name: "an unframed completion cut off mid-object",
			body: `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"content":"half`,
		},
		{
			name: "a reply that started no choice at all",
			body: `{"id":"resp","choices":[]}`,
			want: true,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := IsCacheableResponse(200, []byte(testCase.body)); got != testCase.want {
				t.Errorf("IsCacheableResponse(200, %q) = %v, want %v", testCase.body, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. List every SSE fixture under testdata/sse and check it exists in a manually classified storable/not-storable map.
//  2. Assert the fixture count matches the classified count, so no fixture is missing from the classification.
//  3. For each fixture, assert IsCacheableResponse(200, ...) matches its classified expectation.
func TestEveryRecordedStreamIsClassified(t *testing.T) {
	t.Parallel()
	storable := map[string]bool{
		"comment_and_blank_lines.sse":   false,
		"completion_wrapped_stream.sse": true,
		"content_stream.sse":            true,
		"kimi_thinking_stream.sse":      true,
		"logprobs_stream.sse":           true,
		"malformed_data_line.sse":       false,
		"newlineless_final_content.sse": false,
		"newlineless_final_error.sse":   false,
		"token_ids_stream.sse":          true,
		"tool_calls_stream.sse":         true,
	}
	entries, err := os.ReadDir(filepath.Join("testdata", "sse"))
	if err != nil {
		t.Fatalf("reading fixtures: %v", err)
	}
	entries = slices.DeleteFunc(entries, func(entry os.DirEntry) bool { return entry.IsDir() })
	if len(entries) != len(storable) {
		t.Fatalf("%d fixtures against %d classified: the list and the directory must say the same", len(entries), len(storable))
	}
	for _, entry := range entries {
		want, classified := storable[entry.Name()]
		if !classified {
			t.Fatalf("%s is unclassified: say whether the cache must store it", entry.Name())
		}
		t.Run(entry.Name(), func(t *testing.T) {
			if got := IsCacheableResponse(200, readSSEFixture(t, entry.Name())); got != want {
				t.Fatalf("IsCacheableResponse = %v, want %v", got, want)
			}
		})
	}
}

// Test flow:
//  1. Call CacheRefusal with a table of status/body pairs covering a finished success, an empty body, transient and non-momentary failures, unreadable bodies and choices, an unfinished answer, and a disallowed status both with a finished and an unfinished body.
//  2. Assert each case returns the matching named refusal reason (or CacheStorable).
func TestCacheRefusalNamesWhyItRefused(t *testing.T) {
	t.Parallel()
	finished := `{"choices":[{"index":0,"message":{"content":"hi"},"finish_reason":"stop"}]}`
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{name: "a finished success", status: 200, body: finished, want: CacheStorable},
		{name: "no body at all", status: 200, body: "", want: CacheRefusedEmptyBody},
		{name: "a transient failure", status: 200, body: `{"error":{"message":"service unavailable"}}`, want: CacheRefusedFailure},
		{name: "a failure that names no moment, under a success status", status: 200, body: `{"error":{"message":"empty content stream"}}`, want: CacheRefusedFailure},
		{name: "an event nobody can read", status: 200, body: "data: {\"choices\":[{\"index\":0,\n\n", want: CacheRefusedUnreadable},
		{name: "a body nothing can read", status: 200, body: "<html>upstream is down</html>", want: CacheRefusedUnreadable},
		{name: "choices no client could render", status: 200, body: `{"choices":"none"}`, want: CacheRefusedUnreadable},
		{name: "the same, inside an event", status: 200, body: "data: {\"choices\":\"none\"}\n\n", want: CacheRefusedUnreadable},
		{name: "an answer that stopped mid-answer", status: 200, body: `{"choices":[{"index":0,"message":{"content":"half"}}]}`, want: CacheRefusedUnfinished},
		{name: "one choice finished beside one still running", status: 200, body: `{"choices":[{"index":0,"message":{"content":"a"},"finish_reason":"stop"},{"index":1,"message":{"content":"b"}}]}`, want: CacheRefusedUnfinished},
		{name: "a status no replay may carry", status: 500, body: finished, want: CacheRefusedStatus},
		{name: "a status refused before the answer is judged", status: 500, body: `{"choices":[{"index":0,"message":{"content":"half"}}]}`, want: CacheRefusedStatus},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := CacheRefusal(testCase.status, []byte(testCase.body)); got != testCase.want {
				t.Errorf("CacheRefusal(%d, %q) = %q, want %q", testCase.status, testCase.body, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build a response body naming maxIndexedElements choices, and separately one with maxIndexedElements+1 choices, every choice finished.
//  2. Call IsCacheableResponse(200, ...) on each.
//  3. Assert the body at the limit is cacheable and the one past it is not.
func TestAFloodOfChoicesIsNotStored(t *testing.T) {
	t.Parallel()
	for _, choices := range []int{maxIndexedElements, maxIndexedElements + 1} {
		t.Run(fmt.Sprint(choices), func(t *testing.T) {
			var body strings.Builder
			body.WriteString(`{"choices":[`)
			for index := range choices {
				if index > 0 {
					body.WriteString(",")
				}
				fmt.Fprintf(&body, `{"index":%d,"message":{"content":"x"},"finish_reason":"stop"}`, index)
			}
			body.WriteString(`]}`)

			want := choices <= maxIndexedElements
			if got := IsCacheableResponse(200, []byte(body.String())); got != want {
				t.Fatalf("IsCacheableResponse over %d choices = %v, want %v", choices, got, want)
			}
		})
	}
}

// Test flow:
//  1. Build an SSE event whose logprobs field carries a bareword -Infinity value.
//  2. Rewrite it through a StreamRewriter with logprobs and alternatives requested.
//  3. Assert the rewritten output no longer contains "Infinity".
//  4. Assert CacheRefusal(200, ...) on the rewritten output reports CacheStorable.
func TestAStreamCarryingBarewordsStaysCacheableAfterTheStrip(t *testing.T) {
	t.Parallel()
	event := []byte(`data: {"choices":[{"index":0,"delta":{"content":"ok"},` +
		`"logprobs":{"content":[{"token":"ok","logprob":-Infinity}]},"finish_reason":"stop"}]}` + "\n\n")

	rewritten, err := NewStreamRewriter(LogprobIntent{Keep: true, KeepTop: true}, true).Write(event)
	if err != nil {
		t.Fatalf("Write(): %v", err)
	}

	if bytes.Contains(rewritten, []byte("Infinity")) {
		t.Fatalf("a bareword no decoder reads reached the cache: %s", rewritten)
	}
	if got := CacheRefusal(200, rewritten); got != CacheStorable {
		t.Fatalf("CacheRefusal = %q, want the reply stored", got)
	}
}
