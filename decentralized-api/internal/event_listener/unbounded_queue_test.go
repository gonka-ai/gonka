package event_listener

import (
	"context"
	"decentralized-api/logging"
	"fmt"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBasicQueueOperations verifies basic enqueue/dequeue functionality
func TestBasicQueueOperations(t *testing.T) {
	q := NewUnboundedQueue[int]()
	defer q.Close()

	// Test sending and receiving a single item
	q.In <- 42
	result := <-q.Out
	if result != 42 {
		t.Errorf("Expected 42, got %d", result)
	}

	// Test sending and receiving multiple items in order
	values := []int{1, 2, 3, 4, 5}
	for _, v := range values {
		q.In <- v
	}

	for i, expected := range values {
		result := <-q.Out
		if result != expected {
			t.Errorf("Item %d: Expected %d, got %d", i, expected, result)
		}
	}
}

// TestQueueClosing verifies that the queue closes properly
func TestQueueClosing(t *testing.T) {
	q := NewUnboundedQueue[int]()

	// Add some items
	for i := 0; i < 10; i++ {
		q.In <- i
	}

	// Give the queue time to process the inputs
	time.Sleep(50 * time.Millisecond)

	// Close the queue
	q.Close()

	// Collect all the items we receive
	received := make([]int, 0)
	for value := range q.Out {
		received = append(received, value)
	}

	// Verify we received all items
	if len(received) != 10 {
		t.Errorf("Expected to receive 10 items, got %d", len(received))
	}

	// Verify they're in the right order
	for i := 0; i < len(received); i++ {
		if received[i] != i {
			t.Errorf("At position %d: Expected %d, got %d", i, i, received[i])
		}
	}

	// Verify the channel is now closed (already checked by exiting the range loop)
}

// TestConcurrentAccess tests that the queue works correctly with multiple producers and consumers
func TestConcurrentAccess(t *testing.T) {
	q := NewUnboundedQueue[int]()
	defer q.Close()

	const (
		producerCount    = 5
		itemsPerProducer = 100
		totalItems       = producerCount * itemsPerProducer
	)

	// Setup tracking for received items
	var received sync.Map
	var receivedCount atomic.Int64
	var wgConsumers sync.WaitGroup

	// Create a channel to signal consumers when all items are received
	allItemsReceived := make(chan struct{})

	// Start consumers
	wgConsumers.Add(3)
	for i := 0; i < 3; i++ {
		go func() {
			defer wgConsumers.Done()
			for {
				select {
				case item, ok := <-q.Out:
					if !ok {
						// Queue output was closed
						return
					}

					received.Store(item, true)
					count := receivedCount.Add(1)

					// If we've received all items, signal it
					if count >= totalItems {
						close(allItemsReceived)
						return
					}
				case <-allItemsReceived:
					// Another consumer got the last item
					return
				}
			}
		}()
	}

	// Start producers
	var wgProducers sync.WaitGroup
	wgProducers.Add(producerCount)
	for p := 0; p < producerCount; p++ {
		go func(producerID int) {
			defer wgProducers.Done()
			baseValue := producerID * itemsPerProducer

			for i := 0; i < itemsPerProducer; i++ {
				q.In <- baseValue + i

				// Add some randomness to test concurrency
				if rand.Intn(10) > 7 {
					time.Sleep(time.Millisecond)
				}
			}
		}(p)
	}

	// Wait for producers to finish
	wgProducers.Wait()

	// Wait for consumers to process all items or timeout
	select {
	case <-allItemsReceived:
		// Success, all items consumed
	case <-time.After(5 * time.Second):
		t.Fatalf("Timeout waiting for consumers, received %d/%d items", receivedCount.Load(), totalItems)
	}

	// Now wait for all consumer goroutines to exit
	waitCh := make(chan struct{})
	go func() {
		wgConsumers.Wait()
		close(waitCh)
	}()

	select {
	case <-waitCh:
		// All consumers have exited properly
	case <-time.After(1 * time.Second):
		t.Log("Warning: Timeout waiting for consumer goroutines to exit")
	}

	// Verify all items were received exactly once
	if count := receivedCount.Load(); count != totalItems {
		t.Errorf("Expected %d items, got %d", totalItems, count)
	}

	// Verify each individual item was received
	for p := 0; p < producerCount; p++ {
		baseValue := p * itemsPerProducer
		for i := 0; i < itemsPerProducer; i++ {
			value := baseValue + i
			if _, ok := received.Load(value); !ok {
				t.Errorf("Item %d was not received", value)
			}
		}
	}
}

// TestQueueSizeApproximation verifies that Size() gives a reasonable approximation
func TestQueueSizeApproximation(t *testing.T) {
	q := NewUnboundedQueue[int]()
	defer q.Close()

	// Queue should start empty
	if size := q.Size(); size != 0 {
		t.Errorf("Expected empty queue, got size %d", size)
	}

	// Add some items
	const itemCount = 50
	for i := 0; i < itemCount; i++ {
		q.In <- i
	}

	// Allow time for items to be processed by the manager goroutine
	time.Sleep(100 * time.Millisecond)

	// Size should reflect *approximately* the number of items
	// We can't test for exact equality due to the concurrent nature
	size := q.Size()
	if size == 0 {
		t.Error("Queue size is 0 after adding items")
	}

	// Take items out
	for i := 0; i < itemCount; i++ {
		<-q.Out
	}

	// Allow time for processing
	time.Sleep(100 * time.Millisecond)

	// Size should be approximately 0 again
	if size := q.Size(); size > 0 {
		t.Errorf("Expected empty queue after removing all items, got size %d", size)
	}
}

// TestQueueOrdering verifies that items are dequeued in the same order they were enqueued
func TestQueueOrdering(t *testing.T) {
	q := NewUnboundedQueue[string]()
	defer q.Close()

	// Insert items with distinct values
	items := []string{"first", "second", "third", "fourth", "fifth"}
	for _, item := range items {
		q.In <- item
	}

	// Verify they come out in the same order
	for i, expected := range items {
		result := <-q.Out
		if result != expected {
			t.Errorf("Item %d: Expected '%s', got '%s'", i, expected, result)
		}
	}
}

// TestLargeItemCount verifies the queue can handle a large number of items
func TestLargeItemCount(t *testing.T) {
	q := NewUnboundedQueue[int]()
	defer q.Close()

	const itemCount = 10000

	// Send large batch of items
	go func() {
		for i := 0; i < itemCount; i++ {
			q.In <- i
		}
	}()

	// Receive and verify items
	for i := 0; i < itemCount; i++ {
		value := <-q.Out
		if value != i {
			t.Errorf("Item %d: Expected %d, got %d", i, i, value)
			break
		}
	}
}

// TestTypeVariance verifies the queue works with different data types
func TestTypeVariance(t *testing.T) {
	// Test with string type
	t.Run("StringQueue", func(t *testing.T) {
		q := NewUnboundedQueue[string]()
		defer q.Close()

		q.In <- "hello"
		q.In <- "world"

		if result := <-q.Out; result != "hello" {
			t.Errorf("Expected 'hello', got '%s'", result)
		}
		if result := <-q.Out; result != "world" {
			t.Errorf("Expected 'world', got '%s'", result)
		}
	})

	// Test with custom struct type
	t.Run("StructQueue", func(t *testing.T) {
		type Person struct {
			Name string
			Age  int
		}

		q := NewUnboundedQueue[Person]()
		defer q.Close()

		alice := Person{Name: "Alice", Age: 30}
		bob := Person{Name: "Bob", Age: 25}

		q.In <- alice
		q.In <- bob

		if result := <-q.Out; result.Name != "Alice" || result.Age != 30 {
			t.Errorf("Expected Alice(30), got %v", result)
		}
		if result := <-q.Out; result.Name != "Bob" || result.Age != 25 {
			t.Errorf("Expected Bob(25), got %v", result)
		}
	})
}

// TestEmptyQueueBehavior verifies that trying to receive from an empty queue blocks
func TestEmptyQueueBehavior(t *testing.T) {
	q := NewUnboundedQueue[int]()
	defer q.Close()

	// Try to receive with timeout
	received := false
	go func() {
		<-q.Out
		received = true
	}()

	// Should time out without receiving
	time.Sleep(100 * time.Millisecond)
	if received {
		t.Error("Received value from empty queue, expected to block")
	}

	// Now send a value
	q.In <- 42

	// Allow time for processing
	time.Sleep(100 * time.Millisecond)

	if !received {
		t.Error("Did not receive value after it was sent")
	}
}

func TestCloseQueueTwice(t *testing.T) {
	q := NewUnboundedQueue[int]()

	// Close the queue twice
	q.Close()
	q.Close()

	// No panic should occur
}

func TestQueueMemoryManagement(t *testing.T) {
	q := NewUnboundedQueue[int]()

	const (
		producerCount    = 4
		itemsPerProducer = 1_000_000 // 1M items per producer
		totalItems       = producerCount * itemsPerProducer
	)

	// Log initial memory state
	runtime.GC()
	logMemoryStats("initial")

	// Start consumers first (they don't store items)
	var wg sync.WaitGroup
	consumeDone := make(chan struct{})
	itemsProcessed := atomic.Int64{}

	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case _, ok := <-q.Out:
					if !ok {
						return
					}
					if itemsProcessed.Add(1) == int64(totalItems) {
						close(consumeDone)
						return
					}
				case <-consumeDone:
					return
				}
			}
		}()
	}

	// Log memory before producing
	logMemoryStats("before producing")

	beforeMemory := getAllocatedMemory()
	// Produce items
	for p := 0; p < producerCount; p++ {
		go func(producerID int) {
			base := producerID * itemsPerProducer
			for i := 0; i < itemsPerProducer; i++ {
				q.In <- base + i
			}
		}(p)
	}

	// Wait for all items to be consumed
	select {
	case <-consumeDone:
		// Success
	case <-time.After(30 * time.Second):
		t.Fatal("Timeout waiting for items to be consumed")
	}

	// Log memory during peak
	logMemoryStats("during peak")

	// Force GC and check memory again
	runtime.GC()
	logMemoryStats("after GC")

	// Close queue and wait for consumers
	q.Close()
	wg.Wait()

	// Final memory check after everything is done
	runtime.GC()
	logMemoryStats("final")

	finalMemory := getAllocatedMemory()
	const maxMemoryIncrease = 104_857 // 0.1MB in bytes (ish)
	println("Memory increase " + formatSize(finalMemory-beforeMemory))
	require.Less(t, finalMemory, beforeMemory+maxMemoryIncrease, "Memory usage increased by more than 0.1MB")

	if processed := itemsProcessed.Load(); processed != int64(totalItems) {
		t.Errorf("Expected to process %d items, but processed %d", totalItems, processed)
	}
}

// TestQueueStress runs a stress test with multiple producers and consumers
func TestQueueStress(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	q := NewUnboundedQueue[int]()
	defer q.Close()

	const (
		producerCount    = 10
		consumerCount    = 5
		itemsPerProducer = 1000
		totalItems       = producerCount * itemsPerProducer
	)

	var (
		wgConsumers   sync.WaitGroup
		wgProducers   sync.WaitGroup
		receivedItems sync.Map
		itemCount     atomic.Int64
	)

	// Create a channel to signal all consumers when we've received everything
	allItemsReceived := make(chan struct{})

	// Start consumers
	wgConsumers.Add(consumerCount)
	for i := 0; i < consumerCount; i++ {
		go func(id int) {
			defer wgConsumers.Done()
			for {
				select {
				case item, ok := <-q.Out:
					if !ok {
						// Queue output was closed
						return
					}

					receivedItems.Store(item, true)
					count := itemCount.Add(1)

					// If we've received all items, signal it
					if count >= totalItems {
						close(allItemsReceived)
						return
					}
				case <-allItemsReceived:
					// Another consumer got the last item
					return
				}
			}
		}(i)
	}

	// Start producers
	wgProducers.Add(producerCount)
	for p := 0; p < producerCount; p++ {
		go func(producerID int) {
			defer wgProducers.Done()
			base := producerID * itemsPerProducer

			for i := 0; i < itemsPerProducer; i++ {
				q.In <- base + i

				// Add some randomness to test concurrency patterns
				if rand.Intn(100) > 95 {
					time.Sleep(time.Millisecond)
				}
			}
		}(p)
	}
	// Wait for producers to finish
	wgProducers.Wait()

	// Wait for consumers to process all items or timeout
	select {
	case <-allItemsReceived:
		// Success, all items consumed
	case <-time.After(10 * time.Second):
		t.Fatalf("Timeout waiting for consumers, received %d/%d items", itemCount.Load(), totalItems)
	}

	// Now wait for all consumer goroutines to exit
	waitCh := make(chan struct{})
	go func() {
		wgConsumers.Wait()
		close(waitCh)
	}()

	select {
	case <-waitCh:
		// All consumers have exited properly
	case <-time.After(1 * time.Second):
		t.Log("Warning: Timeout waiting for consumer goroutines to exit")
	}

	// Verify all items were received exactly once
	if count := itemCount.Load(); count != totalItems {
		t.Errorf("Expected %d items, got %d", totalItems, count)
	}

	// Verify each individual item was received
	missingItems := 0
	for p := 0; p < producerCount; p++ {
		base := p * itemsPerProducer
		for i := 0; i < itemsPerProducer; i++ {
			item := base + i
			if _, ok := receivedItems.Load(item); !ok {
				if missingItems < 10 {
					t.Errorf("Item %d was not received", item)
				}
				missingItems++
			}
		}
	}

	if missingItems > 0 {
		t.Errorf("Total of %d items were not received", missingItems)
	}
}

// TestQueueWithDelayedConsumers tests that items are properly queued when consumers are slow
func TestQueueWithDelayedConsumers(t *testing.T) {
	q := NewUnboundedQueue[int]()
	defer q.Close()

	const itemCount = 100

	// Send items rapidly
	for i := 0; i < itemCount; i++ {
		q.In <- i
	}

	// Consume items slowly
	for i := 0; i < itemCount; i++ {
		value := <-q.Out
		if value != i {
			t.Errorf("Expected %d, got %d", i, value)
		}

		// Simulate slow consumer
		if i%10 == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestQueueWithDelayedProducers(t *testing.T) {
	q := NewUnboundedQueue[int]()
	defer q.Close()

	const itemCount = 50

	// Start a consumer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < itemCount; i++ {
			value := <-q.Out
			if value != i {
				t.Errorf("Expected %d, got %d", i, value)
			}
		}
	}()

	// Send items with delays
	for i := 0; i < itemCount; i++ {
		q.In <- i

		// Simulate slow producer
		if i%5 == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Wait for consumer to finish
	wg.Wait()
}

// TestQueueInterface verifies the exported interface works as expected
func TestQueueInterface(t *testing.T) {
	// This test validates that we can use the In/Out channels as documented
	q := NewUnboundedQueue[string]()
	defer q.Close()

	// Producer
	in := q.In
	in <- "message1"
	in <- "message2"

	// Consumer
	out := q.Out
	if msg := <-out; msg != "message1" {
		t.Errorf("Expected 'message1', got '%s'", msg)
	}
	if msg := <-out; msg != "message2" {
		t.Errorf("Expected 'message2', got '%s'", msg)
	}
}

// TestQueueWithTimeout tests behavior with context timeouts
func TestQueueWithTimeout(t *testing.T) {
	q := NewUnboundedQueue[int]()
	defer q.Close()

	// Receive with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	select {
	case <-q.Out:
		t.Error("Received unexpected value from empty queue")
	case <-ctx.Done():
		// Expected behavior
	}

	// Now send and receive with timeout
	q.In <- 42

	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	select {
	case val := <-q.Out:
		if val != 42 {
			t.Errorf("Expected 42, got %d", val)
		}
	case <-ctx.Done():
		t.Error("Timeout while waiting for value")
	}
}

// benchmarkQueueThroughput measures the throughput of the queue
func BenchmarkQueueThroughput(b *testing.B) {
	q := NewUnboundedQueue[int]()
	defer q.Close()

	b.ResetTimer()

	// Start consumer
	go func() {
		for i := 0; i < b.N; i++ {
			<-q.Out
		}
	}()

	// Producer
	for i := 0; i < b.N; i++ {
		q.In <- i
	}
}

// BenchmarkQueueLatency measures the latency of the queue
func BenchmarkQueueLatency(b *testing.B) {
	q := NewUnboundedQueue[int]()
	defer q.Close()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		q.In <- i
		<-q.Out
	}
}
func getAllocatedMemory() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.Alloc
}
func logMemoryStats(tag string) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	logging.Info("Memory stats",
		types.EventProcessing,
		"tag", tag,
		"alloc", formatSize(m.Alloc),
		"totalAlloc", formatSize(m.TotalAlloc),
		"sys", formatSize(m.Sys),
		"heapAlloc", formatSize(m.HeapAlloc),
		"heapSys", formatSize(m.HeapSys),
		"heapIdle", formatSize(m.HeapIdle),
		"heapReleased", formatSize(m.HeapReleased))
}

// Helper to format byte sizes in human-readable form
func formatSize(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
