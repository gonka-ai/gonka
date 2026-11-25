package streamevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"

	"common/completionapi"
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

// Test flow:
//  1. Build a read of lines that only mention an envelope key: an error whose `type` is `devshard_meta`, a line with the receipt key nested in an object, a content line holding the escaped key string, a line that is not a JSON object, and a content line with a literal `devshard_receipt`.
//  2. Call `DataLines` on it.
//  3. Assert every line is kept, in order.
func TestDataLinesKeepLinesThatOnlyMentionAnEnvelopeKey(t *testing.T) {
	want := []string{
		`data: {"error":{"message":"x","type":"devshard_meta","code":500}}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"function":{"name":"x","arguments":{"devshard_receipt":{}}}}]}}]}`,
		`data: {"choices":[{"delta":{"content":"say \"devshard_meta\" and \"devshard_receipt\""}}]}`,
		`data: not json "devshard_receipt"`,
		`data: ["devshard_meta"]`,
		`data: {"choices":[{"delta":{"content":"devshard_receipt"}}]}`,
	}
	var events []byte
	for _, line := range want {
		events = append(events, line+"\n\n"...)
	}

	lines := DataLines(events)

	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("DataLines = %q, want %q", lines, want)
	}
}

// Test flow:
//  1. Build a read of a top-level receipt line with a payload, a top-level meta line, one whose key is written with a `\u` escape, and an error line.
//  2. Call `DataLines` on it.
//  3. Assert only the error line is kept.
func TestDataLinesDropEnvelopesByTheirTopLevelKey(t *testing.T) {
	events := []byte("data: {\"devshard_receipt\":{\"nonce\":1},\"height_sync\":{}}\n\n" +
		"data: {\"devshard_meta\":{\"mempool\":\"AA==\"}}\n\n" +
		"data: {\"devshard_\\u006deta\":{}}\n\n" +
		"data: {\"error\":{\"type\":\"devshard_meta\"}}\n\n")

	lines := DataLines(events)

	want := []string{`data: {"error":{"type":"devshard_meta"}}`}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("DataLines = %q, want %q", lines, want)
	}
}

// Test flow:
//  1. Build the data lines a host stores for an error stream whose error object has `"type":"devshard_meta"`, and the read a gateway sees: those lines with a devshard receipt and meta envelope added.
//  2. Take `DataLines` of the read and serialize them with `Payload`.
//  3. Hash the payload with SHA-256 and the stored lines with `StreamedCompletionResponse.GetHash`, as the host does.
//  4. Assert the two hashes are equal.
func TestPayloadOfAStreamThatMentionsAnEnvelopeKeyHashesAsTheHostStoredIt(t *testing.T) {
	stored := []string{
		`data: {"choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"error":{"message":"x","type":"devshard_meta","code":500}}`,
		"data: [DONE]",
	}
	events := []byte("data: " + `{"devshard_receipt":{}}` + "\n\n")
	for _, line := range stored {
		events = append(events, line+"\n\n"...)
	}
	events = append(events, "data: "+`{"devshard_meta":{}}`+"\n\n"...)

	payload, err := Payload(DataLines(events))
	if err != nil {
		t.Fatalf("Payload: %v", err)
	}

	hostHash, err := (&completionapi.StreamedCompletionResponse{Lines: stored}).GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}
	sum := sha256.Sum256(payload)
	if got := hex.EncodeToString(sum[:]); got != hostHash {
		t.Fatalf("payload hash = %s, host hash = %s", got, hostHash)
	}
}
