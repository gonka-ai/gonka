package streamx_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"trainshard/internal/utils/streamx"
)

type blockingWriter struct {
	mu      sync.Mutex
	written bytes.Buffer
	release chan struct{}
	arrived chan struct{}
	once    sync.Once
	err     error
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() {
		close(w.arrived)
		<-w.release
	})
	if w.err != nil {
		return 0, w.err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.written.Write(p)
}

func (w *blockingWriter) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.written.String()
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{release: make(chan struct{}), arrived: make(chan struct{})}
}

func TestBoundedPassesEverythingThroughToAReaderThatKeepsUp(t *testing.T) {

	var out bytes.Buffer
	bounded := streamx.NewBounded(context.Background(), &out, 1<<20)

	for range 100 {
		if _, err := bounded.Write([]byte("line of output\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	if err := bounded.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if lines := strings.Count(out.String(), "line of output"); lines != 100 {
		t.Fatalf("got %d lines, want every one of them", lines)
	}
	if strings.Contains(out.String(), "dropped") {
		t.Fatalf("a reader that keeps up must see no gap, got %q", out.String())
	}
}

func TestBoundedTellsASlowReaderHowMuchItLost(t *testing.T) {

	out := newBlockingWriter()
	bounded := streamx.NewBounded(context.Background(), out, 64)

	if _, err := bounded.Write([]byte("first")); err != nil {
		t.Fatalf("write: %v", err)
	}
	<-out.arrived
	for range 100 {
		if _, err := bounded.Write(bytes.Repeat([]byte("x"), 32)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	close(out.release)

	if err := bounded.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !strings.Contains(out.text(), "bytes dropped") {
		t.Fatalf("a slow reader must be told about the gap, got %q", out.text())
	}
	if written := len(out.text()); written > 1<<10 {
		t.Fatalf("got %d bytes through a 64 byte buffer, want the oldest output dropped", written)
	}
}

func TestBoundedReportsWhatTheReaderRefused(t *testing.T) {

	out := newBlockingWriter()
	out.err = errors.New("the coordinator hung up")
	bounded := streamx.NewBounded(context.Background(), out, 1<<20)

	if _, err := bounded.Write([]byte("output")); err != nil {
		t.Fatalf("write: %v", err)
	}
	<-out.arrived
	close(out.release)

	if err := bounded.Close(); !errors.Is(err, out.err) {
		t.Fatalf("got %v, want the reader's own failure", err)
	}
}

func TestBoundedStopsWhenTheRequestIsCancelled(t *testing.T) {

	ctx, cancel := context.WithCancel(context.Background())
	bounded := streamx.NewBounded(ctx, &bytes.Buffer{}, 1<<20)
	cancel()

	if err := bounded.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want the stream to end with the request", err)
	}
}
