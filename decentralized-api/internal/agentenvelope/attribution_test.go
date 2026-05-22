package agentenvelope

import (
	"sync"
	"testing"
)

// recordingSink keeps every emitted event. Only the single drain goroutine
// writes to it; tests read it after Close, which waits for that goroutine.
type recordingSink struct {
	events []AttributionEvent
}

func (s *recordingSink) Emit(ev AttributionEvent) { s.events = append(s.events, ev) }

func TestAttributionEmitter_HappyPath(t *testing.T) {
	sink := &recordingSink{}
	e := NewAttributionEmitter(8, sink)
	for i := 0; i < 3; i++ {
		e.Enqueue(AttributionEvent{InferenceID: "inf"})
	}
	e.Close()
	if len(sink.events) != 3 {
		t.Errorf("emitted %d events, want 3", len(sink.events))
	}
	if e.Dropped() != 0 {
		t.Errorf("dropped %d, want 0", e.Dropped())
	}
}

// blockingSink blocks inside the first Emit until released, so a test can hold
// the drain goroutine still and fill the bounded channel deterministically.
type blockingSink struct {
	started chan struct{}
	release chan struct{}
	count   int
}

func (s *blockingSink) Emit(ev AttributionEvent) {
	if s.count == 0 {
		close(s.started)
		<-s.release
	}
	s.count++
}

func TestAttributionEmitter_DropsWhenFull(t *testing.T) {
	sink := &blockingSink{started: make(chan struct{}), release: make(chan struct{})}
	e := NewAttributionEmitter(4, sink)

	// The first event is taken by the drain goroutine, which then blocks
	// inside Emit. The channel buffer is now empty.
	e.Enqueue(AttributionEvent{InferenceID: "blocking"})
	<-sink.started

	// Fill the bounded channel (capacity 4).
	for i := 0; i < 4; i++ {
		e.Enqueue(AttributionEvent{InferenceID: "buffered"})
	}
	// Two more events must be dropped.
	e.Enqueue(AttributionEvent{InferenceID: "dropped"})
	e.Enqueue(AttributionEvent{InferenceID: "dropped"})
	if got := e.Dropped(); got != 2 {
		t.Errorf("dropped %d, want 2", got)
	}

	close(sink.release)
	e.Close()
	if sink.count != 5 {
		t.Errorf("emitted %d events, want 5 (1 blocking + 4 buffered)", sink.count)
	}
}

type countingSink struct{ count int }

func (s *countingSink) Emit(ev AttributionEvent) { s.count++ }

func TestAttributionEmitter_ConcurrentProducers(t *testing.T) {
	sink := &countingSink{}
	e := NewAttributionEmitter(1024, sink)

	const producers, perProducer = 8, 2000
	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				e.Enqueue(AttributionEvent{InferenceID: "concurrent"})
			}
		}()
	}
	wg.Wait()
	e.Close()

	total := producers * perProducer
	if sink.count+int(e.Dropped()) != total {
		t.Errorf("emitted %d + dropped %d = %d, want %d",
			sink.count, e.Dropped(), sink.count+int(e.Dropped()), total)
	}
}

func TestAttributionEmitter_EnqueueAfterCloseDrops(t *testing.T) {
	sink := &recordingSink{}
	e := NewAttributionEmitter(8, sink)
	e.Close()
	e.Enqueue(AttributionEvent{InferenceID: "after-close"})
	if e.Dropped() != 1 {
		t.Errorf("dropped %d, want 1", e.Dropped())
	}
	if len(sink.events) != 0 {
		t.Errorf("emitted %d events after close, want 0", len(sink.events))
	}
}

func TestAttributionEmitter_CloseIdempotent(t *testing.T) {
	e := NewAttributionEmitter(8, &recordingSink{})
	e.Close()
	e.Close()
}

// panicSink panics on its first Emit. An unrecovered panic in the drain
// goroutine would terminate the process, so the emitter must isolate it.
type panicSink struct{ calls int }

func (s *panicSink) Emit(ev AttributionEvent) {
	s.calls++
	if s.calls == 1 {
		panic("sink failure")
	}
}

func TestAttributionEmitter_SinkPanicDoesNotKillDrain(t *testing.T) {
	sink := &panicSink{}
	e := NewAttributionEmitter(8, sink)
	for i := 0; i < 3; i++ {
		e.Enqueue(AttributionEvent{InferenceID: "x"})
	}
	e.Close()
	if sink.calls != 3 {
		t.Errorf("sink Emit called %d times, want 3 (drain survived the panic)", sink.calls)
	}
}
