package streamevidence

import (
	"reflect"
	"testing"
)

// Test flow:
//  1. Build a read carrying a devshard receipt line, an error line, a devshard meta line and `[DONE]`, each ended by a blank line.
//  2. Call `DataLines` on it.
//  3. Assert only the error line and `[DONE]` are kept, in order.
func TestDataLinesSkipTheDevshardEnvelopes(t *testing.T) {
	events := []byte("data: {\"devshard_receipt\":{}}\n\n" +
		"data: {\"error\":{\"code\":500,\"message\":\"x\",\"type\":\"InternalServerError\"}}\n\n" +
		"data: {\"devshard_meta\":{}}\n\n" +
		"data: [DONE]\n\n")

	lines := DataLines(events)

	want := []string{`data: {"error":{"code":500,"message":"x","type":"InternalServerError"}}`, "data: [DONE]"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("DataLines = %q, want %q", lines, want)
	}
}

// Test flow:
//  1. Build a read with carriage-return line ends, a comment line, an event line, a line that names `data` without its space, and a data line with no final newline.
//  2. Call `DataLines` on it.
//  3. Assert the two data lines are kept with their carriage returns trimmed and every other line is dropped.
func TestDataLinesKeepOnlyDataLinesWithoutCarriageReturns(t *testing.T) {
	events := []byte(": keep-alive\r\n" +
		"event: message\r\n" +
		"data: {\"choices\":[]}\r\n\r\n" +
		"data:{\"choices\":[]}\r\n" +
		"data: {\"error\":{\"message\":\"boom\"}}")

	lines := DataLines(events)

	want := []string{`data: {"choices":[]}`, `data: {"error":{"message":"boom"}}`}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("DataLines = %q, want %q", lines, want)
	}
}

// Test flow:
//  1. Call `DataLines` on an empty read and on a read with no data line.
//  2. Assert both return nil.
func TestDataLinesOfAReadWithoutDataAreNil(t *testing.T) {
	for _, events := range [][]byte{nil, []byte(": keep-alive\n\n")} {
		if lines := DataLines(events); lines != nil {
			t.Errorf("DataLines(%q) = %q, want nil", events, lines)
		}
	}
}

// Test flow:
//  1. For each line in the table (the terminator, the terminator with surrounding spaces, a content line, and a line naming `[DONE]` inside its JSON), call `IsDoneLine`.
//  2. Assert only the two terminator forms report true.
func TestIsDoneLineRecognisesTheTerminator(t *testing.T) {
	for _, testCase := range []struct {
		line       string
		wantIsDone bool
	}{
		{line: "data: [DONE]", wantIsDone: true},
		{line: "data:  [DONE] ", wantIsDone: true},
		{line: `data: {"choices":[{"delta":{"content":"hi"}}]}`, wantIsDone: false},
		{line: `data: {"choices":[{"delta":{"content":"[DONE]"}}]}`, wantIsDone: false},
	} {
		if isDone := IsDoneLine(testCase.line); isDone != testCase.wantIsDone {
			t.Errorf("IsDoneLine(%q) = %v, want %v", testCase.line, isDone, testCase.wantIsDone)
		}
	}
}
