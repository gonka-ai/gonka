package event_listener

import (
	"fmt"
	"sync"
	"time"
)

// UnboundedQueue[T] represents an unbounded thread-safe FIFO queue
// that exposes channels for enqueuing and dequeuing elements of type T
type UnboundedQueue[T any] struct {
	// Public channels for interacting with the queue
	In  chan<- T // Send-only channel for producers
	Out <-chan T // Receive-only channel for consumers

	// Private implementation details
	input  chan T
	output chan T
	done   chan struct{}
	wg     sync.WaitGroup
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
func (q *UnboundedQueue[T]) Close() {
	close(q.done)
	close(q.input) // Stop accepting new items
	q.wg.Wait()    // Wait for the manager to finish
}

func main() {
	// Create a string queue
	stringQueue := NewUnboundedQueue[string]()
	defer stringQueue.Close()

	// Create an int queue
	intQueue := NewUnboundedQueue[int]()
	defer intQueue.Close()

	// Wait group to coordinate test completion
	var wg sync.WaitGroup

	// Example with string queue
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Producer
		go func() {
			for i := 0; i < 5; i++ {
				item := fmt.Sprintf("Item-%d", i)
				stringQueue.In <- item
				fmt.Printf("String queue - Enqueued: %s\n", item)
				time.Sleep(100 * time.Millisecond)
			}
		}()

		// Consumer
		for i := 0; i < 5; i++ {
			select {
			case item, ok := <-stringQueue.Out:
				if !ok {
					return
				}
				fmt.Printf("String queue - Received: %s\n", item)
			case <-time.After(1 * time.Second):
				fmt.Println("String queue - Timeout")
				return
			}
		}
	}()

	// Example with int queue
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Producer
		go func() {
			for i := 0; i < 5; i++ {
				intQueue.In <- i * 10
				fmt.Printf("Int queue - Enqueued: %d\n", i*10)
				time.Sleep(100 * time.Millisecond)
			}
		}()

		// Consumer
		for i := 0; i < 5; i++ {
			select {
			case item, ok := <-intQueue.Out:
				if !ok {
					return
				}
				fmt.Printf("Int queue - Received: %d\n", item)
			case <-time.After(1 * time.Second):
				fmt.Println("Int queue - Timeout")
				return
			}
		}
	}()

	// Wait for all operations to complete
	wg.Wait()
	fmt.Println("All operations completed")
}
