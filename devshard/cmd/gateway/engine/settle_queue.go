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
	recheck func(func()) func()
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
	if task.wake == nil && task.recheck == nil {
		delay := max(task.deadline().Sub(q.now()), firesAtOnce)
		q.after(delay, func() { q.fire(task, limit) })
		return
	}
	var (
		mu     sync.Mutex
		fired  bool
		timer  *time.Timer
		undoes []func()
	)
	// The first of the timer, the wake and a recheck fires; the others are dropped so they stop holding the task.
	fireOnce := func() {
		mu.Lock()
		if fired {
			mu.Unlock()
			return
		}
		fired = true
		stop, undo := timer, undoes
		mu.Unlock()
		if stop != nil {
			stop.Stop()
		}
		for _, drop := range undo {
			drop()
		}
		q.fire(task, limit)
	}
	mu.Lock()
	defer mu.Unlock()
	if task.wake != nil {
		unregister := context.AfterFunc(task.wake, fireOnce)
		undoes = append(undoes, func() { unregister() })
	}
	// Subscribed before the deadline is read, so a finish landing in between is not missed.
	if task.recheck != nil {
		undoes = append(undoes, task.recheck(fireOnce))
	}
	delay := max(task.deadline().Sub(q.now()), firesAtOnce)
	timer = q.after(delay, fireOnce)
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
