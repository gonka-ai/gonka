package scheduler

import (
	"sync"
	"sync/atomic"
	"time"
)

// Decision is match's total outcome for one nonce: exactly one of serve, burn, hold, or decline. See rules.md, "1. A committed nonce is always settled".
type Decision interface{ isDecision() }

type serve struct {
	waiter           *waiter
	despiteExclusion bool
}
type burn struct{ kind GhostKind }

// hold leaves the bound nonce uncommitted until then, giving a compatible waiter a chance. See README, "The match decision".
type hold struct{ until time.Time }

// decline gives the nonce back uncommitted: with no live waiter, a burn would name a reason that never happened.
type decline struct{}

func (serve) isDecision()   {}
func (burn) isDecision()    {}
func (hold) isDecision()    {}
func (decline) isDecision() {}

// waiter is one request queued on an escrow's dispatcher; handoff guards bound and orders deliver against abandon. See routing.md, "The per-escrow dispatcher".
type waiter struct {
	profile   RequestProfile
	exclude   map[string]bool
	enqueued  time.Time
	replyCh   chan pickResult
	abandoned atomic.Bool
	bound     bool

	handoff sync.Mutex
}

// newWaiter buffers replyCh so the dispatcher's handoff never blocks on the caller.
func newWaiter(profile RequestProfile, enqueued time.Time) *waiter {
	queued := &waiter{
		profile:  profile,
		enqueued: enqueued,
		replyCh:  make(chan pickResult, 1),
	}
	if len(profile.Exclude) > 0 {
		queued.exclude = make(map[string]bool, len(profile.Exclude))
		for _, participant := range profile.Exclude {
			queued.exclude[participant] = true
		}
	}
	return queued
}

// deliver never blocks: a full or abandoned reply channel means the caller is gone.
func (w *waiter) deliver(result pickResult) (accepted bool) {
	w.handoff.Lock()
	defer w.handoff.Unlock()
	if w.abandoned.Load() {
		return false
	}
	select {
	case w.replyCh <- result:
		return true
	default:
		return false
	}
}

func (w *waiter) bind() bool {
	w.handoff.Lock()
	defer w.handoff.Unlock()
	if w.abandoned.Load() {
		return false
	}
	w.bound = true
	return true
}

func (w *waiter) abandon() (delivered pickResult, wasDelivered bool) {
	w.handoff.Lock()
	if w.bound {
		w.handoff.Unlock()
		return <-w.replyCh, true
	}
	defer w.handoff.Unlock()
	w.abandoned.Store(true)
	select {
	case result := <-w.replyCh:
		return result, true
	default:
		return pickResult{}, false
	}
}

type pickResult struct {
	assignment Assignment
	err        error
}
