package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const replayBudgetBodies = 3

type heldAtWrite struct {
	*httptest.ResponseRecorder
	budget  *BufferBudget
	written bool
	held    int64
}

func (w *heldAtWrite) Write(chunk []byte) (int, error) {
	if !w.written {
		w.written = true
		w.held = w.budget.Held()
	}
	return w.ResponseRecorder.Write(chunk)
}

func (w *heldAtWrite) Flush() {}

// Test flow:
//  1. Give the gateway a body budget and start a streamed request from a caller whose client leaves while the race goes on.
//  2. Repeat the identical request, wait until it has taken the live stream over, and assert the budget holds none of its body while the race still runs.
//  3. Let the race finish and repeat the request once more, and assert the budget held none of its body when the cached reply was first written.
func TestAReplayDoesNotKeepTheCallersBodyChargedToItsShare(t *testing.T) {
	fixture := newAdoptionFixture(t, 1, succeeded())
	live := fixture.live
	live.server.requestBodies = NewBufferBudget(int64(len(streamChatBody)) * replayBudgetBodies)
	fixture.startAndLeave(t, "caller-a")

	_, served := fixture.startAdopter(t, t.Context())

	if held := live.server.requestBodies.Held(); held != 0 {
		t.Errorf("body bytes reserved while the adopted stream is followed = %d, want 0", held)
	}
	fixture.finishRace()
	<-served
	cached := &heldAtWrite{ResponseRecorder: httptest.NewRecorder(), budget: live.server.requestBodies}
	live.requestInto(t, cached, http.MethodPost, chatPath, streamChatBody, callerHeaders("caller-a"))

	if !cached.written || cached.Body.String() != fullStream {
		t.Fatalf("cached replay = %q, written %v, want the whole stream", cached.Body.String(), cached.written)
	}
	if cached.held != 0 {
		t.Errorf("body bytes reserved when the cached reply was written = %d, want 0", cached.held)
	}
	assertNothingReserved(t, live, "after both replays")
}
