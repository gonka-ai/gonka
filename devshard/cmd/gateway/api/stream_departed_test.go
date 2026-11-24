package api

import (
	"bytes"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/filters"
)

type socketFailingAfter struct {
	*httptest.ResponseRecorder
	writesLeft int
}

func (s *socketFailingAfter) Write(chunk []byte) (int, error) {
	if s.writesLeft == 0 {
		return 0, io.ErrClosedPipe
	}
	s.writesLeft--
	return s.ResponseRecorder.Write(chunk)
}

// Test flow:
//  1. Build a streamed `clientStream` recording to a log and refuse an event past the carry bound.
//  2. Hand it another chunk the way the engine does once it departed the refused client.
//  3. Assert the socket is still attached, so the handler's failure event still reaches the client.
func TestAClientTheGatewayRefusedKeepsItsSocketWhenTheEngineHandsOnLaterBytes(t *testing.T) {
	log := testLog(newStreamIndex(), nil, testLogLimit)
	stream := newClientStream(httptest.NewRecorder(), "req-1", true, false, filters.LogprobIntent{}, 0, nil)
	stream.recordTo(log)
	if _, err := stream.Write(bytes.Repeat([]byte("x"), maxBufferedResponseBytes+1)); !errors.Is(err, engine.ErrReplyRefused) {
		t.Fatalf("Write() = %v, want a refusal", err)
	}

	stream.WriteDeparted([]byte(firstEvent))

	if !log.socketAttached() {
		t.Fatal("the socket of a refused client was detached before its failure event was written")
	}
}

// Test flow:
//  1. Build a streamed `clientStream` recording to a log, over a socket that fails on its second write.
//  2. Write an event without its blank line, then close the stream: the tail, then a separator and the terminator.
//  3. Assert the log still holds the separator and the terminator after the socket refused the separator.
func TestALogKeepsTheTerminatorWhenTheSocketRefusesTheSeparatorBeforeIt(t *testing.T) {
	log := testLog(newStreamIndex(), nil, testLogLimit)
	socket := &socketFailingAfter{ResponseRecorder: httptest.NewRecorder(), writesLeft: 1}
	stream := newClientStream(socket, "req-1", true, false, filters.LogprobIntent{}, 0, nil)
	stream.recordTo(log)
	if _, err := stream.Write([]byte(strings.TrimSuffix(firstEvent, "\n\n"))); err != nil {
		t.Fatalf("Write() = %v, want the event held until the stream closes", err)
	}

	_ = stream.Close()

	entry, refusal := log.conclude("7", nil)
	if refusal != filters.CacheStorable || !strings.HasSuffix(string(entry.body), "\n\n"+doneEvent) {
		t.Fatalf("log = %q refusal %q, want it to end with a separator and the terminator", entry.body, refusal)
	}
}

// Test flow:
//  1. Build a streamed `clientStream` recording to a log, write one event to its socket, then hand it the rest of the answer the way the engine does once the client left.
//  2. Close it as a departed stream whose race succeeded, so the terminator goes into the log only.
//  3. Assert the log ends with the terminator while `delivered` reports that the client never got it.
func TestATerminatorWrittenOnlyToTheLogOfADepartedClientIsNotReportedAsDelivered(t *testing.T) {
	log := testLog(newStreamIndex(), nil, testLogLimit)
	socket := httptest.NewRecorder()
	stream := newClientStream(socket, "req-1", true, false, filters.LogprobIntent{}, 0, nil)
	stream.recordTo(log)
	if _, err := stream.Write([]byte(firstEvent)); err != nil {
		t.Fatalf("Write() = %v, want the event delivered", err)
	}

	stream.WriteDeparted([]byte(firstEvent + doneEvent))
	stream.finishDetached(engine.RaceOutcome{Succeeded: true})

	entry, refusal := log.conclude("7", nil)
	if refusal != filters.CacheStorable || !strings.HasSuffix(string(entry.body), doneEvent) {
		t.Fatalf("log = %q refusal %q, want it to end with the terminator", entry.body, refusal)
	}
	if _, terminated := stream.delivered(); terminated || strings.Contains(socket.Body.String(), doneEvent) {
		t.Fatalf("delivered terminated = %v with socket body %q, want the terminator reported only when the socket got it", terminated, socket.Body.String())
	}
}
