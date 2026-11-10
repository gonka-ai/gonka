package filters

import "testing"

// Test flow:
//  1. Define a table of streamed buffers: content alone, an error event between content and the terminator, a flat vLLM error framed with CRLF, an error left as an unterminated last event, an error object split across two data lines, an empty error object, and two errors.
//  2. Drop the upstream error events from each buffer.
//  3. Assert every event carrying an error is gone, every other event is kept byte for byte, and a buffer with no error comes back unchanged.
//  4. Assert the first removed error's message is reported, and nothing is reported when no event was removed.
func TestDropUpstreamErrorEventsKeepsOnlyTheEventsWithoutAnError(t *testing.T) {
	const contentEvent = `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"
	testCases := []struct {
		name        string
		events      string
		want        string
		wantDropped bool
		wantMessage string
	}{
		{name: "content alone", events: contentEvent + "data: [DONE]\n\n", want: contentEvent + "data: [DONE]\n\n"},
		{name: "an error between content and the terminator", events: contentEvent + `data: {"error":{"message":"revoked","code":401}}` + "\n\ndata: [DONE]\n\n", want: contentEvent + "data: [DONE]\n\n", wantDropped: true, wantMessage: "revoked"},
		{name: "a flat error framed with CRLF", events: `data: {"object":"error","message":"overloaded","code":503}` + "\r\n\r\n" + contentEvent, want: contentEvent, wantDropped: true, wantMessage: "overloaded"},
		{name: "an unterminated last error", events: contentEvent + `data: {"error":"revoked"}`, want: contentEvent, wantDropped: true, wantMessage: "revoked"},
		{name: "an error split across two data lines", events: contentEvent + "data: {\"error\":\ndata: {\"message\":\"revoked\",\"code\":401}}\n\n", want: contentEvent, wantDropped: true, wantMessage: "revoked"},
		{name: "an empty error object", events: `data: {"error":{}}` + "\n\n" + contentEvent, want: contentEvent, wantDropped: true},
		{name: "two errors", events: `data: {"error":"first"}` + "\n\n" + contentEvent + `data: {"error":"second"}` + "\n\n", want: contentEvent, wantDropped: true, wantMessage: "first"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			kept, first, dropped := DropUpstreamErrorEvents([]byte(testCase.events))
			if string(kept) != testCase.want || dropped != testCase.wantDropped || first.Message != testCase.wantMessage {
				t.Fatalf("DropUpstreamErrorEvents(%q) = %q, %+v, %v; want %q, message %q, %v", testCase.events, kept, first, dropped, testCase.want, testCase.wantMessage, testCase.wantDropped)
			}
		})
	}
}

// Test flow:
//  1. Define a table of streamed buffers: no terminator, the terminator last, the terminator alone, the terminator first with content after it, and the terminator framed with CRLF.
//  2. Trim the terminator from each buffer.
//  3. Assert every `[DONE]` event is gone, every other event is kept in order, and a buffer with no terminator comes back unchanged.
func TestTrimSSEDoneDropsEveryTerminatorAndKeepsTheRest(t *testing.T) {
	const contentEvent = `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"
	testCases := []struct {
		name   string
		events string
		want   string
	}{
		{name: "no terminator", events: contentEvent, want: contentEvent},
		{name: "the terminator last", events: contentEvent + "data: [DONE]\n\n", want: `data: {"choices":[{"delta":{"content":"hi"}}]}`},
		{name: "the terminator alone", events: "data: [DONE]\n\n", want: ""},
		{name: "the terminator first with content after it", events: "data: [DONE]\n\n" + contentEvent, want: `data: {"choices":[{"delta":{"content":"hi"}}]}`},
		{name: "the terminator framed with CRLF", events: contentEvent + "data: [DONE]\r\n\r\n" + contentEvent, want: contentEvent + `data: {"choices":[{"delta":{"content":"hi"}}]}`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := TrimSSEDone([]byte(testCase.events)); string(got) != testCase.want {
				t.Fatalf("TrimSSEDone(%q) = %q, want %q", testCase.events, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Define a table of streams, each a sequence of writes and an optional close: a plain chunk, error events in each shape a host sends (named, flat object, escaped key, split across two data lines, framed with CRLF), an error event split across two writes, an error left as the unterminated tail, and a plain chunk written after an error.
//  2. Feed each stream through a StreamRewriter.
//  3. Assert LastOutputMayCarryUpstreamError, read after the last call, is true for every output holding an error event and false for every output without one.
func TestStreamRewriterFlagsEveryOutputThatMayCarryAnUpstreamError(t *testing.T) {
	const contentEvent = `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"
	testCases := []struct {
		name   string
		writes []string
		close  bool
		want   bool
	}{
		{name: "a plain chunk", writes: []string{contentEvent}},
		{name: "a named error", writes: []string{contentEvent + `data: {"error":{"message":"revoked","code":401}}` + "\n\n"}, want: true},
		{name: "a flat error object", writes: []string{`data: {"object":"error","message":"overloaded","code":503}` + "\n\n"}, want: true},
		{name: "an error key spelled with escapes", writes: []string{`data: {"\u0065rror":"revoked"}` + "\n\n"}, want: true},
		{name: "an error split across two data lines", writes: []string{"data: {\"error\":\ndata: {\"message\":\"revoked\"}}\n\n"}, want: true},
		{name: "an error framed with CRLF", writes: []string{`data: {"error":"revoked"}` + "\r\n\r\n"}, want: true},
		{name: "an error event split across two writes", writes: []string{`data: {"error":`, `"revoked"}` + "\n\n"}, want: true},
		{name: "an error left as the unterminated tail", writes: []string{contentEvent + `data: {"error":"revoked"}`}, close: true, want: true},
		{name: "a plain chunk after an error", writes: []string{`data: {"error":"revoked"}` + "\n\n", contentEvent}},
		{name: "a close with no tail after an error", writes: []string{`data: {"error":"revoked"}` + "\n\n"}, close: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			rewriter := NewStreamRewriter(LogprobIntent{}, false, 0)
			for _, write := range testCase.writes {
				if _, err := rewriter.Write([]byte(write)); err != nil {
					t.Fatalf("Write(%q) = %v", write, err)
				}
			}
			if testCase.close {
				if _, err := rewriter.Close(); err != nil {
					t.Fatalf("Close() = %v", err)
				}
			}
			if got := rewriter.LastOutputMayCarryUpstreamError(); got != testCase.want {
				t.Fatalf("LastOutputMayCarryUpstreamError() = %v after %q, want %v", got, testCase.writes, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Generate chunks and feed each through a StreamRewriter, then close it.
//  2. After the write and after the close, take every output the rewriter does not flag as possibly carrying an upstream error.
//  3. Assert DropUpstreamErrorEvents finds no error in it: the flag only ever clears an output the drop would have left unchanged.
func FuzzLastOutputMayCarryUpstreamErrorAgreesWithTheDrop(f *testing.F) {
	f.Add([]byte(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"))
	f.Add([]byte(`data: {"error":{"message":"revoked","code":401}}` + "\n\n"))
	f.Add([]byte(`data: {"object":"error","message":"x"}` + "\r\n\r\n"))
	f.Add([]byte("data: {\"error\":\ndata: {\"message\":\"revoked\"}}\n\n"))
	f.Add([]byte("data: [DONE]\n\ndata: {\"Error\":\"revoked\"}"))
	f.Add([]byte(`data: {"\u0065rror":"revoked"}` + "\n\n"))
	f.Fuzz(func(t *testing.T, chunk []byte) {
		rewriter := NewStreamRewriter(LogprobIntent{}, false, 0)
		written, _ := rewriter.Write(chunk)
		assertClearedOutputCarriesNoError(t, rewriter, written)
		tail, _ := rewriter.Close()
		assertClearedOutputCarriesNoError(t, rewriter, tail)
	})
}

func assertClearedOutputCarriesNoError(t *testing.T, rewriter *StreamRewriter, output []byte) {
	t.Helper()
	if rewriter.LastOutputMayCarryUpstreamError() {
		return
	}
	if _, failure, dropped := DropUpstreamErrorEvents(output); dropped {
		t.Fatalf("the rewriter cleared %q, which carries the upstream error %+v", output, failure)
	}
}
