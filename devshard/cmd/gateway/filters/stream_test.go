package filters

import (
	"bytes"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// feedInChunks streams data through rewriter in fixed-size pieces and returns everything emitted.
func feedInChunks(t *testing.T, rewriter *StreamRewriter, data []byte, chunkSize int) []byte {
	t.Helper()
	var emitted bytes.Buffer
	for start := 0; start < len(data); start += chunkSize {
		end := min(start+chunkSize, len(data))
		out, err := rewriter.Write(data[start:end])
		if err != nil {
			t.Fatalf("Write(%d:%d) = %v", start, end, err)
		}
		emitted.Write(out)
	}
	final, err := rewriter.Close()
	if err != nil {
		t.Fatalf("Close() = %v", err)
	}
	emitted.Write(final)
	return emitted.Bytes()
}

func assertNoInternalFields(t *testing.T, out []byte) {
	t.Helper()
	for _, field := range []string{"logprob", "token_ids", "prompt_logprobs"} {
		if bytes.Contains(out, []byte(field)) {
			t.Errorf("output leaked %q: %s", field, out)
		}
	}
}

// Test flow:
//  1. For each field in `clientStrippedFields`, write an event carrying that field alone alongside a `content` sibling.
//  2. Assert the field never leaks to the client output, since the rewriter's cheap byte pre-check must catch every field on its own rather than relying on a recognized sibling.
//  3. Assert the sibling `content` field survives.
func TestStreamRewriter_StripsEveryFieldEvenWhenItArrivesAlone(t *testing.T) {
	for _, field := range clientStrippedFields {
		t.Run(field, func(t *testing.T) {
			event := []byte(`data: {"choices":[{"delta":{"content":"ok","` + field + `":[1,2]}}]}` + "\n\n")

			out, err := NewStreamRewriter(LogprobIntent{}, true).Write(event)
			if err != nil {
				t.Fatalf("Write() = %v", err)
			}

			if bytes.Contains(out, []byte(`"`+field+`"`)) {
				t.Fatalf("%q leaked to the client: %s", field, out)
			}
			if !bytes.Contains(out, []byte(`"content":"ok"`)) {
				t.Fatalf("sibling content field was lost: %s", out)
			}
		})
	}
}

// Test flow:
//  1. Run table cases of an event framed with and without a space after `data:`, after an `event:` or `id:` line, after a comment line, and with CRLF line endings.
//  2. Feed each framing through the rewriter one chunk at a time.
//  3. Assert every framing strips internal fields while the sibling `content` field survives.
func TestStreamRewriter_StripsEveryDataLineFraming(t *testing.T) {
	payload := `{"choices":[{"delta":{"content":"ok"},"token_ids":[7]}]}`
	cases := []struct {
		name  string
		event string
	}{
		{name: "with_a_space", event: "data: " + payload + "\n\n"},
		{name: "without_a_space", event: "data:" + payload + "\n\n"},
		{name: "after_an_event_line", event: "event: message\ndata: " + payload + "\n\n"},
		{name: "after_an_id_line", event: "id: 42\ndata: " + payload + "\n\n"},
		{name: "after_a_comment_line", event: ": keep-alive\ndata: " + payload + "\n\n"},
		{name: "crlf_framed_without_a_space", event: "id: 42\r\ndata:" + payload + "\r\n\r\n"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := feedInChunks(t, NewStreamRewriter(LogprobIntent{}, true), []byte(testCase.event), len(testCase.event))

			assertNoInternalFields(t, got)
			if !bytes.Contains(got, []byte(`"content":"ok"`)) {
				t.Errorf("sibling content field was lost: %s", got)
			}
		})
	}
}

// Test flow:
//  1. Read the `completion_wrapped_stream.sse` fixture, an SSE-wrapped complete response, since an OpenAI streaming client reads choices[].delta and renders nothing from a wrapped message.
//  2. Rewrite the whole stream.
//  3. Assert no internal fields leak and the wrapped message is converted into delta chunks.
//  4. Assert the synthesized stream matches byte-for-byte, including the `usage` object rebuilt in alphabetical order from a map.
func TestStreamRewriter_WrappedCompletionBecomesChunks(t *testing.T) {
	stream := readSSEFixture(t, "completion_wrapped_stream.sse")

	got := rewriteWholeStream(t, stream)

	assertNoInternalFields(t, got)
	if bytes.Contains(got, []byte(`"message"`)) {
		t.Errorf("the client was handed a message where it reads a delta: %s", got)
	}
	want := strings.Join([]string{
		`data: {"id":"chatcmpl-cw1","object":"chat.completion.chunk","created":13,"model":"model-a","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-cw1","object":"chat.completion.chunk","created":13,"model":"model-a","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-cw1","object":"chat.completion.chunk","created":13,"model":"model-a","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"chatcmpl-cw1","object":"chat.completion.chunk","created":13,"model":"model-a","choices":[],"usage":{"completion_tokens":2,"prompt_tokens":3,"total_tokens":5}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")
	if string(got) != want {
		t.Errorf("synthesised stream\n got:  %s\n want: %s", got, want)
	}
}

// Test flow:
//  1. Write an already-chunked event whose content quotes the word "message", and an error event carrying a `message` field.
//  2. Assert both events pass through the rewriter verbatim, unconverted.
func TestStreamRewriter_ChunkEventsAndErrorsAreNotConverted(t *testing.T) {
	cases := []struct {
		name  string
		event string
	}{
		{
			name:  "a_chunk_quoting_the_word_message",
			event: `data: {"choices":[{"delta":{"content":"the \"message\" field"}}]}` + "\n\n",
		},
		{
			name:  "an_error_carrying_a_message_field",
			event: `data: {"error":{"message":"boom","type":"server_error"}}` + "\n\n",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := NewStreamRewriter(LogprobIntent{}, true).Write([]byte(testCase.event))
			if err != nil {
				t.Fatalf("Write() = %v", err)
			}

			if string(got) != testCase.event {
				t.Errorf("Write() = %s, want the event verbatim", got)
			}
		})
	}
}

// Test flow:
//  1. Split a fixture event's bytes mid-way through its `top_logprobs` field.
//  2. Write the two halves to the rewriter in two separate calls.
//  3. Assert the first (incomplete) write emits nothing, and the joined output strips internal fields while keeping the sibling `content`.
func TestStreamRewriter_SplitFrameIsRewritten(t *testing.T) {
	stream := readSSEFixture(t, "logprobs_stream.sse")
	target := splitCompleteEvents(t, stream)[1]
	splitAt := bytes.Index(target, []byte("top_logprobs")) + len("top_logprobs")
	if splitAt <= len("top_logprobs") || splitAt >= len(target) {
		t.Fatalf("fixture no longer contains a mid-event top_logprobs split point")
	}
	rewriter := NewStreamRewriter(LogprobIntent{}, true)

	firstOut, err := rewriter.Write(target[:splitAt])
	if err != nil {
		t.Fatalf("first Write() = %v", err)
	}
	secondOut, err := rewriter.Write(target[splitAt:])
	if err != nil {
		t.Fatalf("second Write() = %v", err)
	}

	if len(firstOut) != 0 {
		t.Errorf("incomplete event must be retained, got %q", firstOut)
	}
	joined := append(append([]byte(nil), firstOut...), secondOut...)
	assertNoInternalFields(t, joined)
	if !bytes.Contains(joined, []byte(`"content":"ok"`)) {
		t.Errorf("sibling content field was lost: %s", joined)
	}
}

// Test flow:
//  1. Feed the `logprobs_stream.sse` fixture through the rewriter one byte at a time.
//  2. Assert internal fields are stripped, the sibling `content` survives, and the output ends with the `[DONE]` marker.
func TestStreamRewriter_ByteByByteFeedStripsLogprobs(t *testing.T) {
	stream := readSSEFixture(t, "logprobs_stream.sse")

	got := feedInChunks(t, NewStreamRewriter(LogprobIntent{}, true), stream, 1)

	assertNoInternalFields(t, got)
	if !bytes.Contains(got, []byte(`"content":"ok"`)) {
		t.Errorf("sibling content field was lost: %s", got)
	}
	if !bytes.HasSuffix(got, []byte("data: [DONE]\n\n")) {
		t.Errorf("[DONE] marker not preserved: %s", got)
	}
}

// Test flow:
//  1. For each of several SSE fixtures and several chunk sizes, feed the stream through the rewriter both in one write and split into chunks.
//  2. Assert the chunked output matches the whole-stream output byte-for-byte.
func TestStreamRewriter_ChunkSizeDoesNotChangeOutput(t *testing.T) {
	fixtures := []string{
		"content_stream.sse",
		"tool_calls_stream.sse",
		"logprobs_stream.sse",
		"token_ids_stream.sse",
		"comment_and_blank_lines.sse",
		"newlineless_final_content.sse",
		"completion_wrapped_stream.sse",
	}
	for _, name := range fixtures {
		for _, chunkSize := range []int{1, 3, 17, 4096} {
			t.Run(fmt.Sprintf("%s/chunk=%d", name, chunkSize), func(t *testing.T) {
				stream := readSSEFixture(t, name)
				want := rewriteWholeStream(t, stream)

				got := feedInChunks(t, NewStreamRewriter(LogprobIntent{}, true), stream, chunkSize)

				if !bytes.Equal(got, want) {
					t.Errorf("chunked output differs from whole-stream output\n got:  %q\n want: %q", got, want)
				}
			})
		}
	}
}

// Test flow:
//  1. Write the entire `token_ids_stream.sse` fixture to the rewriter in a single call.
//  2. Assert internal fields are stripped and the expected number of events is emitted.
func TestStreamRewriter_SeveralFramesInOneChunk(t *testing.T) {
	stream := readSSEFixture(t, "token_ids_stream.sse")
	rewriter := NewStreamRewriter(LogprobIntent{}, true)

	got, err := rewriter.Write(stream)
	if err != nil {
		t.Fatalf("Write() = %v", err)
	}

	assertNoInternalFields(t, got)
	if want := 4; bytes.Count(got, []byte("data: ")) != want {
		t.Errorf("emitted %d events, want %d: %s", bytes.Count(got, []byte("data: ")), want, got)
	}
}

// Test flow:
//  1. Write a fixture event's bytes with its terminator stripped off.
//  2. Write the terminator separately.
//  3. Assert nothing is emitted until the terminator arrives, and the event is then emitted with internal fields stripped.
func TestStreamRewriter_RetainsPartialUntilTerminatorArrives(t *testing.T) {
	stream := readSSEFixture(t, "logprobs_stream.sse")
	target := splitCompleteEvents(t, stream)[1]
	rewriter := NewStreamRewriter(LogprobIntent{}, true)

	withoutTerminator, err := rewriter.Write(bytes.TrimRight(target, "\n"))
	if err != nil {
		t.Fatalf("Write() = %v", err)
	}
	afterTerminator, err := rewriter.Write([]byte("\n\n"))
	if err != nil {
		t.Fatalf("Write() = %v", err)
	}

	if len(withoutTerminator) != 0 {
		t.Errorf("unterminated event must not be emitted, got %q", withoutTerminator)
	}
	if len(afterTerminator) == 0 {
		t.Error("event was not emitted once its terminator arrived")
	}
	assertNoInternalFields(t, afterTerminator)
}

// Test flow:
//  1. Feed a CRLF-terminated event followed by a CRLF `[DONE]` marker through the rewriter at several chunk sizes.
//  2. Assert internal fields are stripped, the sibling `content` survives, and the CRLF `[DONE]` marker is preserved.
func TestStreamRewriter_CRLFTerminatedEventIsRewritten(t *testing.T) {
	event := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"logprobs\":{\"content\":[]}}]}\r\n\r\ndata: [DONE]\r\n\r\n")
	for _, chunkSize := range []int{1, 3, len(event)} {
		t.Run(fmt.Sprintf("chunk=%d", chunkSize), func(t *testing.T) {
			got := feedInChunks(t, NewStreamRewriter(LogprobIntent{}, true), event, chunkSize)

			assertNoInternalFields(t, got)
			if !bytes.Contains(got, []byte(`"content":"ok"`)) {
				t.Errorf("sibling content field was lost: %s", got)
			}
			if !bytes.HasSuffix(got, []byte("data: [DONE]\r\n\r\n")) {
				t.Errorf("[DONE] marker not preserved: %q", got)
			}
		})
	}
}

// Test flow:
//  1. Write exactly `MaxStreamCarryBytes` of unterminated data to the rewriter.
//  2. Write one more byte past the cap.
//  3. Assert the at-cap write emits nothing without error, the overflow write fails with `ErrStreamCarryOverflow`, and the carry buffer is released rather than retained.
func TestStreamRewriter_CarryOverflowFailsInsteadOfGrowing(t *testing.T) {
	rewriter := NewStreamRewriter(LogprobIntent{}, true)
	atCap, err := rewriter.Write(bytes.Repeat([]byte("x"), MaxStreamCarryBytes))
	if err != nil {
		t.Fatalf("Write() at the cap = %v, want no error", err)
	}
	if len(atCap) != 0 {
		t.Errorf("unterminated bytes must not be emitted, got %d bytes", len(atCap))
	}

	overflow, err := rewriter.Write([]byte("x"))

	if !errors.Is(err, ErrStreamCarryOverflow) {
		t.Fatalf("Write() past the cap = %v, want ErrStreamCarryOverflow", err)
	}
	if len(overflow) != 0 {
		t.Errorf("overflowing Write emitted %d bytes, want none", len(overflow))
	}
	if len(rewriter.carry) != 0 {
		t.Errorf("carry kept %d bytes after overflow, want it released", len(rewriter.carry))
	}
}

// Test flow:
//  1. Overflow the rewriter's carry buffer.
//  2. Write a well-formed fixture stream and then close the rewriter.
//  3. Assert both the write and the close continue to fail with `ErrStreamCarryOverflow` and emit nothing.
func TestStreamRewriter_StaysFailedAfterOverflow(t *testing.T) {
	rewriter := NewStreamRewriter(LogprobIntent{}, true)
	if _, err := rewriter.Write(bytes.Repeat([]byte("x"), MaxStreamCarryBytes+1)); !errors.Is(err, ErrStreamCarryOverflow) {
		t.Fatalf("Write() past the cap = %v, want ErrStreamCarryOverflow", err)
	}

	afterOverflow, writeErr := rewriter.Write(readSSEFixture(t, "logprobs_stream.sse"))
	closed, closeErr := rewriter.Close()

	if !errors.Is(writeErr, ErrStreamCarryOverflow) || len(afterOverflow) != 0 {
		t.Errorf("Write() after overflow = %q, %v; want no output and ErrStreamCarryOverflow", afterOverflow, writeErr)
	}
	if !errors.Is(closeErr, ErrStreamCarryOverflow) || len(closed) != 0 {
		t.Errorf("Close() after overflow = %q, %v; want no output and ErrStreamCarryOverflow", closed, closeErr)
	}
}

// Test flow:
//  1. Write a complete, properly terminated fixture stream to the rewriter.
//  2. Close the rewriter.
//  3. Assert Close emits nothing and returns no error.
func TestStreamRewriter_CloseOnCleanEndEmitsNothing(t *testing.T) {
	rewriter := NewStreamRewriter(LogprobIntent{}, true)
	if _, err := rewriter.Write(readSSEFixture(t, "logprobs_stream.sse")); err != nil {
		t.Fatalf("Write() = %v", err)
	}

	final, err := rewriter.Close()
	if err != nil {
		t.Errorf("Close() = %v, want no error", err)
	}
	if len(final) != 0 {
		t.Errorf("Close() emitted %q, want nothing", final)
	}
}

// Test flow:
//  1. Write the `newlineless_final_content.sse` fixture, whose final event has no trailing terminator.
//  2. Close the rewriter.
//  3. Assert the write and close outputs together reproduce the original stream exactly.
func TestStreamRewriter_CloseEmitsUnterminatedFinalEvent(t *testing.T) {
	stream := readSSEFixture(t, "newlineless_final_content.sse")
	rewriter := NewStreamRewriter(LogprobIntent{}, true)
	emitted, err := rewriter.Write(stream)
	if err != nil {
		t.Fatalf("Write() = %v", err)
	}

	final, err := rewriter.Close()
	if err != nil {
		t.Errorf("Close() = %v, want no error", err)
	}
	if !bytes.Equal(append(emitted, final...), stream) {
		t.Errorf("well-formed unterminated final event was not preserved\n got:  %q\n want: %q", append(emitted, final...), stream)
	}
}

// Test flow:
//  1. Write an event with no trailing terminator carrying an internal `token_ids` field.
//  2. Close the rewriter.
//  3. Assert the closed output strips the internal field while the sibling `content` survives.
func TestStreamRewriter_CloseRewritesUnterminatedFinalEvent(t *testing.T) {
	event := []byte(`data: {"choices":[{"delta":{"content":"ok"},"token_ids":[7]}]}`)
	rewriter := NewStreamRewriter(LogprobIntent{}, true)
	if _, err := rewriter.Write(event); err != nil {
		t.Fatalf("Write() = %v", err)
	}

	final, err := rewriter.Close()
	if err != nil {
		t.Errorf("Close() = %v, want no error", err)
	}
	assertNoInternalFields(t, final)
	if !bytes.Contains(final, []byte(`"content":"ok"`)) {
		t.Errorf("sibling content field was lost: %s", final)
	}
}

// Test flow:
//  1. Write a truncated, unparseable final event.
//  2. Close the rewriter.
//  3. Assert Close fails with `ErrStreamTruncatedEvent` and forwards nothing.
func TestStreamRewriter_CloseDropsTruncatedFinalEvent(t *testing.T) {
	truncated := []byte(`data: {"choices":[{"delta":{"content":"ok"},"logprobs":{"content":[{"logprob":-0.1`)
	rewriter := NewStreamRewriter(LogprobIntent{}, true)
	if _, err := rewriter.Write(truncated); err != nil {
		t.Fatalf("Write() = %v", err)
	}

	final, err := rewriter.Close()

	if !errors.Is(err, ErrStreamTruncatedEvent) {
		t.Errorf("Close() = %v, want ErrStreamTruncatedEvent", err)
	}
	if len(final) != 0 {
		t.Errorf("Close() forwarded a truncated event: %q", final)
	}
}

// Test flow:
//  1. Write an unterminated comment line.
//  2. Close the rewriter.
//  3. Assert Close returns the comment line verbatim.
func TestStreamRewriter_CloseKeepsUnterminatedNonDataLine(t *testing.T) {
	comment := []byte(": keep-alive")
	rewriter := NewStreamRewriter(LogprobIntent{}, true)
	if _, err := rewriter.Write(comment); err != nil {
		t.Fatalf("Write() = %v", err)
	}

	final, err := rewriter.Close()
	if err != nil {
		t.Errorf("Close() = %v, want no error", err)
	}
	if !bytes.Equal(final, comment) {
		t.Errorf("Close() = %q, want the comment line verbatim", final)
	}
}

// Test flow:
//  1. Feed the `malformed_data_line.sse` fixture through the rewriter one byte at a time.
//  2. Assert the malformed event is dropped rather than forwarded, and the stream still ends with `[DONE]`.
func TestStreamRewriter_MalformedFrameIsDropped(t *testing.T) {
	stream := readSSEFixture(t, "malformed_data_line.sse")

	got := feedInChunks(t, NewStreamRewriter(LogprobIntent{}, true), stream, 1)

	assertNoInternalFields(t, got)
	if bytes.Contains(got, []byte(`"broken":true`)) {
		t.Errorf("malformed event was forwarded: %s", got)
	}
	if !bytes.HasSuffix(got, []byte("data: [DONE]\n\n")) {
		t.Errorf("[DONE] marker not preserved: %s", got)
	}
}

// Test flow:
//  1. Write an event whose content text merely mentions the word "logprobs".
//  2. Assert the event passes through the rewriter verbatim.
func TestStreamRewriter_ParseableFrameMentioningLogprobsInTextSurvives(t *testing.T) {
	event := []byte(`data: {"choices":[{"delta":{"content":"the \"logprobs\" field"}}]}` + "\n\n")
	rewriter := NewStreamRewriter(LogprobIntent{}, true)

	got, err := rewriter.Write(event)
	if err != nil {
		t.Fatalf("Write() = %v", err)
	}

	if !bytes.Equal(got, event) {
		t.Errorf("Write() = %q, want the event verbatim", got)
	}
}

// Test flow:
//  1. Run table cases of `assembleSSEBody` against a restated header, a space-less data line, CRLF framing, comment/event lines mixed in, an already-assembled plain JSON body, a terminator-only body, and an empty body.
//  2. Assert each case's exact assembled result.
func TestAssembleSSEBody(t *testing.T) {
	testCases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "a restated header does not accumulate",
			body: "data: {\"id\":\"same\"}\n\ndata: {\"id\":\"same\"}\n\ndata: [DONE]\n\n",
			want: `{"id":"same","object":"chat.completion"}`,
		},
		{
			name: "a space-less data line",
			body: "data:{\"id\":\"tight\"}\n\n",
			want: `{"id":"tight","object":"chat.completion"}`,
		},
		{
			name: "crlf framing",
			body: "data: {\"id\":\"crlf\"}\r\n\r\ndata: [DONE]\r\n\r\n",
			want: `{"id":"crlf","object":"chat.completion"}`,
		},
		{
			name: "comment and event lines are not payloads",
			body: ": keep-alive\nevent: message\ndata: {\"id\":\"only\"}\n\n",
			want: `{"id":"only","object":"chat.completion"}`,
		},
		{name: "a plain json body is already assembled", body: `{"id":"plain"}`, want: `{"id":"plain"}`},
		{name: "a terminator and nothing else", body: "data: [DONE]\n\n", want: `{"error":{"message":"no response data"}}`},
		{name: "an empty body", body: "", want: `{"error":{"message":"no response data"}}`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := assembleSSEBody([]byte(testCase.body)); string(got) != testCase.want {
				t.Fatalf("assembleSSEBody(%q) = %s, want %s", testCase.body, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Run table cases of `HasSSEDone` against the terminator, the terminator over CRLF, a plain payload, the marker appearing inside a content delta, and an empty string.
//  2. Assert each case's boolean result.
func TestHasSSEDone(t *testing.T) {
	testCases := []struct {
		name   string
		events string
		want   bool
	}{
		{name: "the terminator", events: "data: [DONE]\n\n", want: true},
		{name: "the terminator over crlf", events: "data: [DONE]\r\n\r\n", want: true},
		{name: "a payload", events: "data: {\"id\":\"x\"}\n\n"},
		{name: "the marker inside a content delta", events: "data: {\"delta\":{\"content\":\"data: [DONE]\"}}\n\n"},
		{name: "nothing", events: ""},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := HasSSEDone([]byte(testCase.events)); got != testCase.want {
				t.Fatalf("HasSSEDone(%q) = %v, want %v", testCase.events, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Write an event whose two data lines each hold a separate, independently parseable JSON object — one with a renderable delta, one with internal fields.
//  2. Assert the rewriter drops the whole event rather than forwarding it, since a client would join the two lines into something no client can parse, carrying whatever the second line hides.
func TestStreamRewriter_DropsAMultiLineEventNoClientCouldParse(t *testing.T) {
	event := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n" +
		"data: {\"prompt_logprobs\":[1,2],\"token_ids\":[7]}\n\n")

	got, err := NewStreamRewriter(LogprobIntent{}, true).Write(event)
	if err != nil {
		t.Fatalf("Write() = %v", err)
	}

	assertNoInternalFields(t, got)
	if len(bytes.TrimSpace(got)) != 0 {
		t.Fatalf("an unparseable event reached the client: %s", got)
	}
}

// Test flow:
//  1. Write an event whose single JSON object is split across two data lines, so neither line parses on its own.
//  2. Assert internal fields are stripped from the rejoined object while the sibling `content` survives.
func TestStreamRewriter_StripsAnObjectSplitAcrossDataLines(t *testing.T) {
	event := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}],\n" +
		"data: \"prompt_logprobs\":[1,2],\"token_ids\":[7]}\n\n")

	got, err := NewStreamRewriter(LogprobIntent{}, true).Write(event)
	if err != nil {
		t.Fatalf("Write() = %v", err)
	}

	assertNoInternalFields(t, got)
	if !bytes.Contains(got, []byte(`"content":"ok"`)) {
		t.Fatalf("the renderable content was lost: %s", got)
	}
}

// Test flow:
//  1. Write an event whose `logprobs` key is spelled with a `\u` escape, confirming the raw-byte pre-check cannot see it as plain text.
//  2. Rewrite the event and decode the result as JSON.
//  3. Assert the escaped `logprobs` key does not survive in the decoded delta while `content` does.
func TestAnEscapedInternalKeyIsStrippedFromAStreamedDelta(t *testing.T) {
	event := []byte(`data: {"choices":[{"delta":{"content":"hi","\u006cogprobs":{"x":1}}}]}` + "\n\n")
	if bytes.Contains(event, []byte("logprobs")) {
		t.Fatal("the key is spelled in plain bytes, so the raw-byte scan finds it and the escape is never exercised")
	}

	rewritten := rewriteEventOnly(event, LogprobIntent{}, true)

	if rewritten == nil {
		t.Fatal("the event was dropped, not stripped")
	}
	var decoded map[string]any
	payload := rewritten[len("data: "):]
	if err := stdjson.Unmarshal(bytes.TrimSpace(payload), &decoded); err != nil {
		t.Fatalf("rewritten event does not parse: %v (%s)", err, rewritten)
	}
	choices, _ := decoded["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices = %v", decoded)
	}
	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)
	if _, leaked := delta["logprobs"]; leaked {
		t.Fatalf("an escaped internal key reached the client: %s", rewritten)
	}
	if delta["content"] != "hi" {
		t.Fatalf("the renderable content did not survive the strip: %s", rewritten)
	}
}

// Test flow:
//  1. Run table cases of a whole chat.completion response whose `id`, `created`, `choices[].index`, or `model` field is given the wrong type.
//  2. Rewrite each event.
//  3. Assert every case still converts to chunks and carries its content, since a host-controlled identity field must not be allowed to fail the conversion and leave a streaming client rendering nothing.
func TestAPoisonedIdentityFieldStillConvertsToChunks(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		event string
	}{
		{name: "numeric_id", event: `data: {"id":123,"object":"chat.completion","choices":[{"index":0,"message":{"content":"hi"}}]}`},
		{name: "created_past_float_range", event: `data: {"id":"x","created":1e999,"object":"chat.completion","choices":[{"index":0,"message":{"content":"hi"}}]}`},
		{name: "string_index", event: `data: {"id":"x","object":"chat.completion","choices":[{"index":"0","message":{"content":"hi"}}]}`},
		{name: "numeric_model", event: `data: {"id":"x","model":7,"object":"chat.completion","choices":[{"index":0,"message":{"content":"hi"}}]}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			rewritten := rewriteEventOnly([]byte(testCase.event+"\n\n"), LogprobIntent{}, true)

			if rewritten == nil {
				t.Fatal("the event was dropped")
			}
			if !bytes.Contains(rewritten, []byte(`"chat.completion.chunk"`)) {
				t.Fatalf("the response was forwarded unconverted, so a streaming client renders nothing: %s", rewritten)
			}
			if !bytes.Contains(rewritten, []byte(`"content":"hi"`)) {
				t.Fatalf("the content did not survive the conversion: %s", rewritten)
			}
		})
	}
}

// Test flow:
//  1. Rewrite a whole chat.completion response whose content contains `< > &`.
//  2. Assert the converted chunk carries that content unescaped, rather than inflated by HTML escaping on a path that carries whole model responses.
func TestConvertedChunksCarryGeneratedContentUnescaped(t *testing.T) {
	event := []byte(`data: {"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"content":"a < b & c"}}]}` + "\n\n")

	rewritten := rewriteEventOnly(event, LogprobIntent{}, true)

	if !bytes.Contains(rewritten, []byte(`"content":"a < b & c"`)) {
		t.Fatalf("generated content was escaped on the way to the client: %s", rewritten)
	}
}

// Test flow:
//  1. Run table cases of `rewriteEventOnly` against the same event with a `LogprobIntent` asking for neither, logprobs only, and logprobs with alternatives.
//  2. Assert the rewritten output carries logprobs and alternatives exactly matching what the intent asked for, and never leaks `token_ids`, since the streaming path strips separately from the buffered one and must follow the same client intent.
func TestTheStreamStripFollowsWhatTheClientAskedFor(t *testing.T) {
	event := []byte(`data: {"choices":[{"delta":{"content":"hi","logprobs":{"content":[{"token":"hi","logprob":-0.5,"top_logprobs":[{"token":"hello"}]}]}},"index":0}],"token_ids":[7]}` + "\n\n")

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

			rewritten := string(rewriteEventOnly(event, testCase.intent, true))

			if held := strings.Contains(rewritten, `"logprob"`); held != testCase.wantLogprobs {
				t.Fatalf("logprob present = %v, want %v: %s", held, testCase.wantLogprobs, rewritten)
			}
			if filled := strings.Contains(rewritten, `"hello"`); filled != testCase.wantTopFilled {
				t.Fatalf("alternatives present = %v, want %v: %s", filled, testCase.wantTopFilled, rewritten)
			}
			if strings.Contains(rewritten, "token_ids") {
				t.Fatalf("an internal field reached the client: %s", rewritten)
			}
		})
	}
}

// Test flow:
//  1. Write a chat.completion event whose `message` key is spelled with a `\u` escape, confirming the raw-byte pre-check cannot see it as plain text.
//  2. Rewrite the event.
//  3. Assert it still converts to chunks, carries its content, and leaks no internal fields.
func TestAnEscapedMessageKeyStillConvertsToChunks(t *testing.T) {
	event := []byte(`data: {"object":"chat.completion","choices":[{"index":0,"\u006dessage":{"content":"hi","\u006cogprobs":{"x":1}}}]}` + "\n\n")
	if bytes.Contains(event, []byte(`"message"`)) {
		t.Fatal("the key is spelled plainly, so the escape is never exercised")
	}

	rewritten := rewriteEventOnly(event, LogprobIntent{}, true)

	if !bytes.Contains(rewritten, []byte(`"chat.completion.chunk"`)) {
		t.Fatalf("the response was forwarded unconverted, so a streaming client renders nothing: %s", rewritten)
	}
	if !bytes.Contains(rewritten, []byte(`"content":"hi"`)) {
		t.Fatalf("the content did not survive: %s", rewritten)
	}
	assertNoInternalFields(t, rewritten)
}

// Test flow:
//  1. Write an event whose content string embeds a raw newline, splitting its payload across two lines with nothing to strip.
//  2. Rewrite the event and extract its data payload.
//  3. Assert the payload still parses as one JSON object and the content survives, rather than being dropped as a line with no `data:` prefix.
func TestAMultiLineEventWithNothingToStripSurvivesIntact(t *testing.T) {
	event := []byte("data: {\"choices\":[{\"delta\":{\"content\":\n" + "data: \"hi\"}}]}\n\n")

	rewritten := rewriteEventOnly(event, LogprobIntent{}, true)

	_, payload, held := eventPayload(rewritten)
	if !held {
		t.Fatalf("the rewritten event carries no data line: %q", rewritten)
	}
	var decoded map[string]any
	if err := stdjson.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("a client cannot rejoin the event into an object: %v (%q)", err, rewritten)
	}
	if !bytes.Contains(rewritten, []byte(`"hi"`)) {
		t.Fatalf("the content was lost: %q", rewritten)
	}
}

// Test flow:
//  1. Rewrite a whole chat.completion response whose logprobs carry the non-finite bareword `-Infinity`.
//  2. Assert it still converts to chunks and carries its content, with the unparseable bareword normalized away before conversion rather than reaching the client.
func TestACompletionWithNonFiniteNumbersStillConvertsToChunks(t *testing.T) {
	event := []byte(`data: {"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"content":"hi"},` +
		`"logprobs":{"content":[{"token":"hi","logprob":-Infinity,"top_logprobs":[{"token":"hi","logprob":-Infinity}]}]}}]}` + "\n\n")

	rewritten := rewriteEventOnly(event, LogprobIntent{Keep: true, KeepTop: true}, true)

	if !bytes.Contains(rewritten, []byte(`"chat.completion.chunk"`)) {
		t.Fatalf("forwarded unconverted, so a streaming client renders nothing while the nonce is paid: %s", rewritten)
	}
	if !bytes.Contains(rewritten, []byte(`"content":"hi"`)) {
		t.Fatalf("the client's answer was lost: %s", rewritten)
	}
	if bytes.Contains(rewritten, []byte("Infinity")) {
		t.Fatalf("a bareword no client can parse reached the client: %s", rewritten)
	}
}

func rewriteEventOnly(event []byte, intent LogprobIntent, keepUsage bool) []byte {
	rewritten, _ := rewriteEvent(event, intent, keepUsage)
	return rewritten
}

// Test flow:
//  1. Run table cases of `completionAsChunks` against a completion carrying the logprobs the client asked for, a completion with `logprobs:null`, and a completion with no `logprobs` field at all.
//  2. Assert every case converts, and that the carried logprobs (or their absence) are preserved rather than dropped, since the client's own intent is applied before this conversion runs.
func TestACompletionConvertedToChunksKeepsTheLogprobsItCarried(t *testing.T) {
	const logprobs = `{"content":[{"token":"ok","logprob":-0.5,"bytes":[111,107],"top_logprobs":[]}]}`
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "logprobs the client asked for",
			body: `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,` +
				`"message":{"role":"assistant","content":"ok"},"logprobs":` + logprobs + `,"finish_reason":"stop"}]}`,
			want: `"logprobs":` + logprobs,
		},
		{
			name: "a host that reported none",
			body: `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,` +
				`"message":{"role":"assistant","content":"ok"},"logprobs":null,"finish_reason":"stop"}]}`,
		},
		{
			name: "a host that named no such field",
			body: `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,` +
				`"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			chunks, converted := completionAsChunks([]byte(testCase.body))
			if !converted {
				t.Fatalf("completionAsChunks() did not convert %s", testCase.body)
			}
			if testCase.want == "" {
				if bytes.Contains(chunks, []byte(`"logprobs"`)) {
					t.Fatalf("a logprobs field appeared where the host sent none: %s", chunks)
				}
				return
			}
			if !bytes.Contains(chunks, []byte(testCase.want)) {
				t.Fatalf("chunks lost the logprobs\n got:  %s\n want: %s", chunks, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Run table cases of a chat.completion response with `Choices`, an empty `choices` beside a capitalised `Choices`, and a capitalised `Message` key.
//  2. Assert every case still converts to chunks and carries its content, regardless of key casing, since the conversion's decoder binds keys case-insensitively.
func TestACompletionConvertsWhateverWayItsKeysAreSpelled(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		event string
	}{
		{name: "capitalised_choices", event: `data: {"id":"x","object":"chat.completion","Choices":[{"index":0,"message":{"content":"hi"}}]}`},
		{name: "empty_choices_beside_a_capitalised_one", event: `data: {"id":"x","object":"chat.completion","choices":[],"Choices":[{"index":0,"message":{"content":"hi"}}]}`},
		{name: "capitalised_message", event: `data: {"id":"x","object":"chat.completion","choices":[{"index":0,"Message":{"content":"hi"}}]}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			rewritten := rewriteEventOnly([]byte(testCase.event+"\n\n"), LogprobIntent{}, true)

			if !bytes.Contains(rewritten, []byte(`"chat.completion.chunk"`)) {
				t.Fatalf("the response was forwarded unconverted, so a streaming client renders nothing: %s", rewritten)
			}
			if !bytes.Contains(rewritten, []byte(`"content":"hi"`)) {
				t.Fatalf("the content did not survive the conversion: %s", rewritten)
			}
		})
	}
}
