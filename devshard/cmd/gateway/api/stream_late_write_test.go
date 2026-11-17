package api

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"devshard/cmd/gateway/filters"
)

// Test flow:
//  1. Build a non-streamed `clientStream` over a shared buffer budget and write part of a reply into it.
//  2. Close it unstarted and discard it, the way the handler does when a race failed before the first byte.
//  3. Write again, as an attempt still running after the race returned would.
//  4. Assert the late write charges nothing to the budget.
func TestALateWriteAfterADiscardedReplyChargesNothing(t *testing.T) {
	budget := NewBufferBudget(1 << 20)
	stream := newClientStream(httptest.NewRecorder(), "req-1", false, false, filters.LogprobIntent{}, budget)
	if _, err := stream.Write(bytes.Repeat([]byte("x"), 900)); err != nil {
		t.Fatalf("Write(): %v", err)
	}
	stream.closeUnstarted()
	stream.discard()

	_, _ = stream.Write(bytes.Repeat([]byte("y"), 700))

	if held := budget.Held(); held != 0 {
		t.Fatalf("budget holds %d bytes after the reply was discarded, want 0", held)
	}
}

// Test flow:
//  1. Build a streamed `clientStream` that has not started, over a cache recorder wrapping the live writer.
//  2. Close it unstarted, discard it and release the recorder's budget, the way the handler does before it returns.
//  3. Write one SSE chunk, as an attempt still running after the handler returned would.
//  4. Assert the chunk reached neither the live writer nor the budget.
func TestALateStreamWriteAfterADiscardedReplyReachesNothing(t *testing.T) {
	budget := NewBufferBudget(1 << 20)
	live := httptest.NewRecorder()
	recorder := newCacheRecorder(live, 1<<20, true, budget)
	stream := newClientStream(recorder, "req-1", true, false, filters.LogprobIntent{}, budget)
	stream.closeUnstarted()
	stream.discard()
	recorder.releaseBudget()

	_, _ = stream.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"late\"}}]}\n\n"))

	if live.Body.Len() != 0 {
		t.Fatalf("live writer got %q after the handler returned, want nothing", live.Body.String())
	}
	if held := budget.Held(); held != 0 {
		t.Fatalf("budget holds %d bytes after the handler returned, want 0", held)
	}
}

// Test flow:
//  1. Build a streamed `clientStream` over a live writer, with nothing written yet.
//  2. Close it unstarted, as the handler does before it writes the error of a race that failed before the first byte.
//  3. Write one SSE chunk before any discard, as an attempt still running would while the handler writes that error.
//  4. Assert the stream reported itself unstarted and the chunk never reached the live writer.
func TestAWriteAfterCloseUnstartedReachesNothingBeforeTheDiscard(t *testing.T) {
	live := httptest.NewRecorder()
	stream := newClientStream(live, "req-1", true, false, filters.LogprobIntent{}, NewBufferBudget(1<<20))

	unstarted := stream.closeUnstarted()
	_, _ = stream.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"late\"}}]}\n\n"))

	if !unstarted {
		t.Fatal("closeUnstarted() = false for a stream that wrote nothing, want true")
	}
	if live.Body.Len() != 0 || live.Header().Get("Content-Type") != "" {
		t.Fatalf("live writer got %q with Content-Type %q after closeUnstarted, want nothing", live.Body.String(), live.Header().Get("Content-Type"))
	}
}

// Test flow:
//  1. Make inference fail before the first byte and hand back a write an attempt that outlived the race makes after `Run` returned.
//  2. POST a streamed chat completion, then run that late write.
//  3. Assert the reply is the 502 alone: the late chunk never reached the client.
func TestAFailedRaceAnswersOnlyItsErrorDespiteALateWrite(t *testing.T) {
	live := newHarness(t)
	live.inference.reply = ""
	live.inference.err = errors.New("every attempt failed")
	live.inference.late = []byte("data: {\"choices\":[{\"delta\":{\"content\":\"late\"}}]}\n\n")
	live.inference.lateWrites = make(chan func(), 1)

	recorder := live.request(t, http.MethodPost, "/v1/chat/completions", streamChatBody, nil)
	(<-live.inference.lateWrites)()

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d, want 502", recorder.Code)
	}
	if body := recorder.Body.String(); strings.Contains(body, "late") {
		t.Fatalf("reply carries the late chunk: %q", body)
	}
}
