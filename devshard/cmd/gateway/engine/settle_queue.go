package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

const firesAtOnce time.Duration = 0

// settleTask is one race's owed votes. See race.md, "The timeout-vote queue".
type settleTask struct {
	deadline func() time.Time
	post     func()
	wake     context.Context
}

func (t settleTask) woken() bool { return t.wake != nil && t.wake.Err() != nil }

// settleQueue holds the votes a shard owes.
type settleQueue struct {
	now   func() time.Time
	after func(time.Duration, func()) *time.Timer

	owed atomic.Int64

	mu      sync.Mutex
	ready   []func()
	posting int
}

func newSettleQueue(now func() time.Time) *settleQueue {
	return &settleQueue{now: now, after: time.AfterFunc}
}

// Add holds a vote the queue owes, and returns before it is posted.
func (q *settleQueue) Add(task settleTask, limit int) {
	q.owed.Add(1)
	q.arm(task, limit)
}

func (q *settleQueue) arm(task settleTask, limit int) {
	delay := max(task.deadline().Sub(q.now()), firesAtOnce)
	if task.wake == nil {
		q.after(delay, func() { q.fire(task, limit) })
		return
	}
	var fired atomic.Bool
	fireOnce := func() {
		if fired.CompareAndSwap(false, true) {
			q.fire(task, limit)
		}
	}
	unregister := context.AfterFunc(task.wake, fireOnce)
	q.after(delay, func() {
		unregister()
		fireOnce()
	})
}

// fire asks the deadline again before taking a place.
func (q *settleQueue) fire(task settleTask, limit int) {
	if !task.woken() && task.deadline().After(q.now()) {
		q.arm(task, limit)
		return
	}
	q.due(task.post, limit)
}

// Owed reports the votes taken and not yet posted.
func (q *settleQueue) Owed() int64 { return q.owed.Load() }

// due posts when a place is free and queues the vote otherwise.
func (q *settleQueue) due(post func(), limit int) {
	q.mu.Lock()
	if limit > 0 && q.posting >= limit {
		q.ready = append(q.ready, post)
		q.mu.Unlock()
		return
	}
	q.posting++
	q.mu.Unlock()
	for post != nil {
		post = q.postThenTake(post)
	}
}

// postThenTake takes the next vote or gives the place back, under one lock.
func (q *settleQueue) postThenTake(post func()) func() {
	post()
	q.owed.Add(-1)
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.ready) == 0 {
		q.posting--
		return nil
	}
	next := q.ready[0]
	q.ready[0] = nil
	q.ready = q.ready[1:]
	if len(q.ready) == 0 {
		q.ready = nil
	}
	return next
}
