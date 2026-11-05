package main

import (
	"context"
	"sync"
)

// perfWriteQueueCapacity bounds the writes waiting for the store; a full
// queue makes the next write wait instead of dropping accounting rows.
const perfWriteQueueCapacity = 1 << 16

// perfWriteQueue applies perf and accounting writes in order on one
// goroutine, so the request path never waits for the store.
type perfWriteQueue struct {
	mu         sync.RWMutex
	isClosed   bool
	operations chan func(context.Context)
	drained    chan struct{}
}

func newPerfWriteQueue(capacity int) *perfWriteQueue {
	queue := &perfWriteQueue{operations: make(chan func(context.Context), capacity), drained: make(chan struct{})}
	go queue.run()
	return queue
}

func (q *perfWriteQueue) run() {
	defer close(q.drained)
	for operation := range q.operations {
		operation(context.Background())
	}
}

func (q *perfWriteQueue) enqueue(operation func(context.Context)) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.isClosed {
		return
	}
	q.operations <- operation
}

// flush returns once every write queued before it has been applied.
func (q *perfWriteQueue) flush(ctx context.Context) error {
	applied := make(chan struct{})
	q.mu.RLock()
	if q.isClosed {
		q.mu.RUnlock()
		return nil
	}
	select {
	case q.operations <- func(context.Context) { close(applied) }:
		q.mu.RUnlock()
	case <-ctx.Done():
		q.mu.RUnlock()
		return ctx.Err()
	}
	select {
	case <-applied:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *perfWriteQueue) close() {
	q.mu.Lock()
	if !q.isClosed {
		q.isClosed = true
		close(q.operations)
	}
	q.mu.Unlock()
	<-q.drained
}
