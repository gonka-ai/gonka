package event_listener

import (
	"sync"
)

// UnboundedQueue[T] represents a thread-safe FIFO queue that exposes channels
// for enqueuing and dequeuing elements of type T.
//
// By default the queue is unbounded (see NewUnboundedQueue). When constructed
// with NewBoundedQueue it instead enforces a maximum number of buffered items
// and applies backpressure to producers: a send on In blocks once the queue is
// full, rather than letting the internal backing slice grow without bound. The
// In/Out channel API, FIFO ordering, and Close semantics are identical in both
// modes, so callers do not need to change how they use the queue.
type UnboundedQueue[T any] struct {
	// Public channels for interacting with the queue
	In  chan<- T // Send-only channel for producers
	Out <-chan T // Receive-only channel for consumers

	// Private implementation details
	input     chan T
	output    chan T
	done      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once // Ensures Close is only executed once
	capacity  int       // 0 = unbounded; >0 = max buffered items before producers block
}

// NewUnboundedQueue creates a new unbounded queue that exposes channels
func NewUnboundedQueue[T any]() *UnboundedQueue[T] {
	return newQueue[T](0)
}

// NewBoundedQueue creates a FIFO queue that applies backpressure to producers
// once `capacity` items are buffered. It preserves the same In/Out channel API
// and ordering guarantees as NewUnboundedQueue; the only behavioral difference
// is that a send on In blocks while the queue is full instead of the backing
// store growing without bound. A capacity <= 0 is treated as unbounded.
//
// Note: because the internal input channel is buffered for performance, the
// effective worst-case number of in-flight items is approximately
// capacity + len(input buffer) + len(output buffer); the bound is intended to
// cap memory, not to be an exact admission-control limit.
func NewBoundedQueue[T any](capacity int) *UnboundedQueue[T] {
	if capacity < 0 {
		capacity = 0
	}
	return newQueue[T](capacity)
}

func newQueue[T any](capacity int) *UnboundedQueue[T] {
	input := make(chan T, 100)  // Buffer size is just for performance
	output := make(chan T, 100) // Buffer size is just for performance
	done := make(chan struct{})

	q := &UnboundedQueue[T]{
		In:       input,  // Public producer channel (send-only)
		Out:      output, // Public consumer channel (receive-only)
		input:    input,  // Private full access
		output:   output, // Private full access
		done:     done,
		capacity: capacity,
	}

	q.wg.Add(1)
	go q.manage() // Start the queue manager goroutine

	return q
}

// manage handles the internal queue operation
func (q *UnboundedQueue[T]) manage() {
	defer q.wg.Done()
	defer close(q.output) // Close output channel when done

	// This slice acts as our unbounded queue storage
	items := make([]T, 0)

	for {
		// If we have items, try to send the first one to output
		// If we don't have items, only wait for input or done
		var out chan T
		var first T

		if len(items) > 0 {
			out = q.output
			first = items[0]
		}

		// Backpressure: when the queue is bounded and full, stop reading from
		// the input channel. Producers then block on their send to In until a
		// consumer drains an item. We keep `out` active so consumers can always
		// make progress and relieve the pressure (so this never deadlocks as
		// long as some consumer keeps draining).
		in := q.input
		if q.capacity > 0 && len(items) >= q.capacity {
			in = nil
		}

		select {
		case item := <-in:
			// Store new item from producer
			items = append(items, item)

		case out <- first:
			// First item was consumed, remove it
			items = items[1:]

		case <-q.done:
			// Shutdown signal received, exit manager
			return
		}
	}
}

// Size returns the approximate number of elements in the queue
// Note: This is approximate since the queue state might change
// immediately after the count is returned
func (q *UnboundedQueue[T]) Size() int {
	// This is just an approximation based on channel buffer lengths
	return len(q.input) + len(q.output)
}

// Close shuts down the queue and waits for the manager to exit
// This method is idempotent and can be safely called multiple times
func (q *UnboundedQueue[T]) Close() {
	q.closeOnce.Do(func() {
		close(q.done)
		close(q.input) // Stop accepting new items
		q.wg.Wait()    // Wait for the manager to finish
	})
}
