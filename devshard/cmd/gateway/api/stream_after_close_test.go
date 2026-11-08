package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"devshard/cmd/gateway/filters"
)

// closedStream returns a clientStream that has already been written to once and closed.
func closedStream(t *testing.T) *clientStream {
	t.Helper()
	stream := newClientStream(httptest.NewRecorder(), "req-1", true, false, filters.LogprobIntent{}, nil)
	if _, err := stream.Write([]byte("data: {\"choices\":[]}\n\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return stream
}

// flushSpy counts flushes instead of touching the underlying writer.
type flushSpy struct {
	http.ResponseWriter
	flushes int
}

func (f *flushSpy) Flush() { f.flushes++ }

// Test flow:
//  1. Create a `clientStream` wrapping a `flushSpy`.
//  2. Write one SSE chunk and close the stream, then record the flush count at that point.
//  3. Call Flush again after close.
//  4. Assert the flush count did not grow, so no flush reaches the writer after close.
func TestClientStreamStopsFlushingAfterClose(t *testing.T) {
	spy := &flushSpy{ResponseWriter: httptest.NewRecorder()}
	stream := newClientStream(spy, "req-1", true, false, filters.LogprobIntent{}, nil)
	if _, err := stream.Write([]byte("data: {\"choices\":[]}\n\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	afterClose := spy.flushes

	stream.Flush()

	if spy.flushes != afterClose {
		t.Errorf("flushed %d more times after close: in production that is the nil-buffer panic",
			spy.flushes-afterClose)
	}
}

// Test flow:
//  1. Build a `closedStream`, a clientStream already written to once and closed.
//  2. Record its delivered byte count, then call Flush and Write one more chunk.
//  3. Assert the late write reports the chunk consumed, not an error.
//  4. Assert delivered bytes did not grow, so the late write never reached the client.
func TestClientStreamIsInertAfterClose(t *testing.T) {
	stream := closedStream(t)
	written, _ := stream.delivered()

	stream.Flush()
	count, err := stream.Write([]byte("data: {\"late\":true}\n\n"))
	if err != nil {
		t.Fatalf("a late write returned %v, want the chunk swallowed", err)
	}
	if count == 0 {
		t.Error("a late write must report the chunk consumed so the producer stops, not retries")
	}
	if after, _ := stream.delivered(); after != written {
		t.Errorf("delivered grew from %d to %d after close: a goroutine outliving the handler reached the client", written, after)
	}
}

// Test flow:
//  1. Build a `closedStream` and record its delivered byte count.
//  2. Call Close a second time.
//  3. Assert delivered bytes stay unchanged, so the second Close wrote nothing new.
func TestClientStreamCloseIsIdempotent(t *testing.T) {
	stream := closedStream(t)
	written, _ := stream.delivered()

	if err := stream.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if after, _ := stream.delivered(); after != written {
		t.Errorf("a second Close wrote %d more bytes, want none", after-written)
	}
}

// Test flow:
//  1. Create a `clientStream` over an `httptest.ResponseRecorder`.
//  2. Run 8 goroutines that each write and flush an SSE chunk 50 times concurrently.
//  3. Close the stream once every goroutine finishes.
//  4. Assert Close returns no error, so the concurrent writers left the stream's internal state intact.
func TestClientStreamSurvivesConcurrentWriters(t *testing.T) {
	stream := newClientStream(httptest.NewRecorder(), "req-1", true, false, filters.LogprobIntent{}, nil)

	var waiting sync.WaitGroup
	for range 8 {
		waiting.Go(func() {
			for range 50 {
				_, _ = stream.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
				stream.Flush()
			}
		})
	}
	waiting.Wait()

	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
