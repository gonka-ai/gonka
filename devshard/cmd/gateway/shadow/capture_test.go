package shadow

import (
	"strings"
	"testing"
	"time"
)

// Test flow:
//  1. Build a capture on a clock that steps 100ms per read and write nothing.
//  2. Assert no first token is reported.
//  3. Write two chunks and assert the first token is the time of the first write only.
func TestCaptureTimesTheFirstWriteOnly(t *testing.T) {
	t.Parallel()
	answer := newCapture(steppedClock())

	if _, streamed := answer.firstToken(); streamed {
		t.Fatal("firstToken streamed = true before any write")
	}
	_, _ = answer.Write([]byte("data: {}\n\n"))
	elapsed, streamed := answer.firstToken()
	_, _ = answer.Write([]byte("data: {}\n\n"))
	again, _ := answer.firstToken()

	if !streamed || elapsed != 100*time.Millisecond || again != elapsed {
		t.Errorf("first token = %v (streamed %v), again %v, want 100ms and unchanged by the second write", elapsed, streamed, again)
	}
}

// Test flow:
//  1. Write an SSE stream whose usage arrives in the last data event, split across writes, with a malformed line and a [DONE] marker.
//  2. Assert the completion tokens are the usage's.
//  3. Write an answer with no usage and assert zero.
func TestCaptureReadsCompletionTokensFromTheUsageEvent(t *testing.T) {
	t.Parallel()
	answer := newCapture(steppedClock())
	for _, chunk := range []string{"data: {\"usage\":{\"compl", "etion_tokens\":12}}\n\n", "data: not json\n\n", "data: [DONE]\n\n"} {
		_, _ = answer.Write([]byte(chunk))
	}
	silent := newCapture(steppedClock())
	_, _ = silent.Write([]byte("data: {\"choices\":[]}\n\n"))

	if got := answer.completionTokens(); got != 12 {
		t.Errorf("completion tokens = %d, want 12", got)
	}
	if got := silent.completionTokens(); got != 0 {
		t.Errorf("completion tokens = %d, want 0 without a usage event", got)
	}
}

// Test flow:
//  1. Write more than the capture's byte ceiling.
//  2. Assert every byte is reported written, so the transport is never short-written, and only the ceiling is kept.
func TestCaptureKeepsOnlyItsCeiling(t *testing.T) {
	t.Parallel()
	answer := newCapture(steppedClock())
	oversized := []byte(strings.Repeat("x", maxCaptureBytes+10))

	written, err := answer.Write(oversized)

	if err != nil || written != len(oversized) || len(answer.body) != maxCaptureBytes {
		t.Errorf("wrote %d of %d (err %v), kept %d, want all written and %d kept", written, len(oversized), err, len(answer.body), maxCaptureBytes)
	}
}

// Test flow:
//  1. Write a stream longer than the capture's ceiling whose usage event comes last, in two writes.
//  2. Assert the completion tokens are still the usage's, since the end of the stream is what is kept.
func TestCaptureReadsTheUsageEventAfterMoreThanItsCeiling(t *testing.T) {
	t.Parallel()
	answer := newCapture(steppedClock())
	filler := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"word \"}}]}\n\n")
	for written := 0; written < 3*maxCaptureBytes; written += len(filler) {
		_, _ = answer.Write(filler)
	}
	for _, chunk := range []string{"data: {\"usage\":{\"completion", "_tokens\":1000}}\n\ndata: [DONE]\n\n"} {
		_, _ = answer.Write([]byte(chunk))
	}

	if got := answer.completionTokens(); got != 1000 || len(answer.body) != maxCaptureBytes {
		t.Errorf("completion tokens = %d, kept %d bytes, want 1000 and %d", got, len(answer.body), maxCaptureBytes)
	}
}
