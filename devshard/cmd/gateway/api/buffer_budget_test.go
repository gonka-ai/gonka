package api

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"devshard/cmd/gateway/filters"
)

// Test flow:
//  1. Create a `BufferBudget` sized to 1KB and two `clientStream`s that share it.
//  2. Write 800 bytes to the first stream and assert the write succeeds.
//  3. Write another 800 bytes to the second stream.
//  4. Assert the second write is refused with `ErrResponseBufferFull`, since together they exceed the shared ceiling.
func TestTheBudgetBoundsEveryBufferedReplyTogether(t *testing.T) {
	budget := NewBufferBudget(1 << 10)
	first := newClientStream(httptest.NewRecorder(), "req-1", false, false, filters.LogprobIntent{}, budget)
	second := newClientStream(httptest.NewRecorder(), "req-2", false, false, filters.LogprobIntent{}, budget)

	if _, err := first.Write(bytes.Repeat([]byte("x"), 800)); err != nil {
		t.Fatalf("the first reply was refused with room to spare: %v", err)
	}

	_, err := second.Write(bytes.Repeat([]byte("y"), 800))
	if !errors.Is(err, ErrResponseBufferFull) {
		t.Fatalf("the second reply was admitted past the ceiling: %v", err)
	}
}

// Test flow:
//  1. Map `ErrResponseBufferFull` through `statusForError`.
//  2. Assert it resolves to HTTP 503 Service Unavailable.
func TestAFullBufferIsAnsweredAsTheShardHavingNoRoom(t *testing.T) {
	if got := statusForError(ErrResponseBufferFull); got != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", got, http.StatusServiceUnavailable)
	}
}

// Test flow:
//  1. Create a budget and a stream, then write 900 bytes to it.
//  2. Discard the stream and assert the budget's held bytes drop to zero.
//  3. Discard the same stream again and assert held bytes stay at zero, so a repeated discard cannot take the budget negative.
func TestDiscardGivesTheBudgetBackOnEveryPath(t *testing.T) {
	budget := NewBufferBudget(1 << 10)
	stream := newClientStream(httptest.NewRecorder(), "req-1", false, false, filters.LogprobIntent{}, budget)
	if _, err := stream.Write(bytes.Repeat([]byte("x"), 900)); err != nil {
		t.Fatalf("Write(): %v", err)
	}

	stream.discard()

	if held := budget.Held(); held != 0 {
		t.Errorf("the budget still holds %d bytes after the reply was discarded", held)
	}
	stream.discard()
	if held := budget.Held(); held != 0 {
		t.Errorf("a second discard took the budget below zero: %d held", held)
	}
}

// Test flow:
//  1. Create a budget and a streaming `clientStream` over it.
//  2. Write one SSE chunk to the stream.
//  3. Assert the budget's held bytes stay at zero, since streaming replies are not buffered.
func TestAStreamingReplyIsNotChargedToTheBudget(t *testing.T) {
	budget := NewBufferBudget(1 << 10)
	stream := newClientStream(httptest.NewRecorder(), "req-1", true, false, filters.LogprobIntent{}, budget)

	if _, err := stream.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")); err != nil {
		t.Fatalf("Write(): %v", err)
	}

	if held := budget.Held(); held != 0 {
		t.Errorf("a streaming reply was charged %d bytes", held)
	}
}

// Test flow:
//  1. Create a budget with a 1MB ceiling and reserve 900 bytes against it.
//  2. Retune the ceiling down to 512 bytes.
//  3. Assert held bytes stay at 900, so lowering the ceiling does not claw back what is already in flight.
//  4. Assert a further reservation is refused under the lowered ceiling.
func TestRetuningDoesNotRepossessWhatIsAlreadyHeld(t *testing.T) {
	budget := NewBufferBudget(1 << 20)
	budget.reserve(900)

	budget.Retune(512)

	if held := budget.Held(); held != 900 {
		t.Errorf("held = %d after the ceiling was lowered, want the 900 still in flight", held)
	}
	if budget.reserve(1) {
		t.Error("the lowered ceiling still admitted a new reply")
	}
}

// Test flow:
//  1. Create a budget with a zero ceiling.
//  2. Reserve a very large amount against it.
//  3. Assert the reservation succeeds, since a zero ceiling means unlimited.
func TestAZeroCeilingHoldsNothingBack(t *testing.T) {
	budget := NewBufferBudget(0)

	if !budget.reserve(1 << 30) {
		t.Error("an unlimited budget refused a reply")
	}
}

// Test flow:
//  1. Start a `newHarness` gateway.
//  2. For each case in the table, varying the request body between a reply the shard serves (`chatBody`) and a body the filters refuse before it reaches a shard, send the request.
//  3. Assert the server's buffer budget holds zero bytes once the response is answered, in both cases.
func TestTheHandlerGivesTheBudgetBackOnceTheReplyIsAnswered(t *testing.T) {
	live := newHarness(t)

	for _, target := range []struct {
		name string
		body string
	}{
		{name: "a reply that was served", body: chatBody},
		{name: "a body the filters refused", body: "{"},
	} {
		t.Run(target.name, func(t *testing.T) {
			live.request(t, http.MethodPost, "/v1/chat/completions", target.body,
				map[string]string{"Authorization": "Bearer " + clientKey})

			if held := live.server.buffers.Held(); held != 0 {
				t.Errorf("the gateway still holds %d buffered bytes after answering", held)
			}
		})
	}
}
