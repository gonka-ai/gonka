package event_listener

import (
	"sync"
)

// UnboundedQueue[T] represents an unbounded thread-safe FIFO queue
// that exposes channels for enqueuing and dequeuing elements of type T
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
}

// NewUnboundedQueue creates a new unbounded queue that exposes channels
func NewUnboundedQueue[T any]() *UnboundedQueue[T] {
	input := make(chan T, 100)  // Buffer size is just for performance
	output := make(chan T, 100) // Buffer size is just for performance
	done := make(chan struct{})

	q := &UnboundedQueue[T]{
		In:     input,  // Public producer channel (send-only)
		Out:    output, // Public consumer channel (receive-only)
		input:  input,  // Private full access
		output: output, // Private full access
		done:   done,
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

		select {
		case item := <-q.input:
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
