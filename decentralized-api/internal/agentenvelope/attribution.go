package agentenvelope

import "sync"

// AttributionSink receives verified attribution events for emission. The
// production wiring adapts Gonka's logging package to this interface; tests
// inject a recording implementation. Keeping the sink behind an interface
// keeps this package free of a logging dependency and keeps it unit testable.
type AttributionSink interface {
	Emit(event AttributionEvent)
}

// AttributionEmitter buffers attribution events on a bounded channel and
// drains them on a single background goroutine, so emission never gates the
// inference request path.
//
// If the queue is full, Enqueue drops the event and increments a counter.
// Drop-on-full is the correct backpressure response for observability data: a
// slow log sink must not stall inference responses. v1 attribution is an
// observability layer, not consensus, so a drop is acceptable and is recorded.
// There is no ordering guarantee, and events can be lost under sink
// backpressure.
type AttributionEmitter struct {
	ch     chan AttributionEvent
	sink   AttributionSink
	wg     sync.WaitGroup
	mu     sync.Mutex
	closed bool
	// dropped is guarded by mu, as is the closed flag and the channel send.
	dropped uint64
}

// NewAttributionEmitter starts an emitter with the given bounded-queue
// capacity and starts its drain goroutine. The caller owns the returned
// emitter and must call Close during shutdown. A capacity below 1 is raised
// to 1.
func NewAttributionEmitter(capacity int, sink AttributionSink) *AttributionEmitter {
	if capacity < 1 {
		capacity = 1
	}
	e := &AttributionEmitter{
		ch:   make(chan AttributionEvent, capacity),
		sink: sink,
	}
	e.wg.Add(1)
	go e.drain()
	return e
}

func (e *AttributionEmitter) drain() {
	defer e.wg.Done()
	for ev := range e.ch {
		e.emitOne(ev)
	}
}

// emitOne forwards one event to the sink. The call is recovered so a panicking
// sink drops a single event rather than crashing the drain goroutine, which in
// Go would terminate the whole process.
func (e *AttributionEmitter) emitOne(ev AttributionEvent) {
	defer func() { _ = recover() }()
	e.sink.Emit(ev)
}

// Enqueue submits an event for asynchronous emission. It never blocks. If the
// queue is full, or the emitter is closed, the event is dropped and the drop
// counter is incremented.
func (e *AttributionEmitter) Enqueue(ev AttributionEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		e.dropped++
		return
	}
	select {
	case e.ch <- ev:
	default:
		e.dropped++
	}
}

// Dropped returns the number of events dropped because the queue was full or
// the emitter was closed.
func (e *AttributionEmitter) Dropped() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dropped
}

// Close stops accepting events, drains the buffered events to the sink, and
// waits for the background goroutine to finish. Close is idempotent.
func (e *AttributionEmitter) Close() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	close(e.ch)
	e.mu.Unlock()
	e.wg.Wait()
}
