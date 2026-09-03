package user

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// inflightVerify is one VerifyTimeout (or error-miss) HTTP call that has
// acquired a queue token and started. Waiters are not inflight.
type inflightVerify struct {
	RequestID string
	Escrow    string
	Nonce     uint64
	Reason    string
	SentAt    time.Time
}

type verifierSlotState struct {
	sem      chan struct{}
	inflight []inflightVerify
	waiters  int
}

// verifierHostQueue serializes outbound VerifyTimeout RPCs per verifier host.
// Each verifier (keyed by validator address) gets a buffered channel acting
// as a semaphore of capacity MaxConcurrentVerifierRPCs. Acquire is
// ctx-aware so a cancelled timeout collection does not stay queued
// forever. Occupancy (inflight + waiters) is stored on the same object so a
// blocked round can log who is holding the slot.
type verifierHostQueue struct {
	mu    sync.Mutex
	slots map[string]*verifierSlotState
}

func newVerifierHostQueue() *verifierHostQueue {
	return &verifierHostQueue{slots: make(map[string]*verifierSlotState)}
}

// SharedVerifierQueue is the process-wide verifier-host limiter. All Sessions
// created with NewSession share it by default, so one proxy runtime cannot
// open more than MaxConcurrentVerifierRPCs concurrent VerifyTimeout RPCs to a
// single verifier across all of its escrows. Tests may inject a private
// queue via WithVerifierQueue to keep assertions isolated.
var SharedVerifierQueue = newVerifierHostQueue()

func (q *verifierHostQueue) state(addr string) *verifierSlotState {
	q.mu.Lock()
	defer q.mu.Unlock()
	st, ok := q.slots[addr]
	if !ok {
		capacity := max(MaxConcurrentVerifierRPCs, 1)
		st = &verifierSlotState{sem: make(chan struct{}, capacity)}
		q.slots[addr] = st
	}
	return st
}

func (q *verifierHostQueue) addWaiter(addr string, delta int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	st := q.slots[addr]
	if st == nil {
		return
	}
	st.waiters += delta
	if st.waiters < 0 {
		st.waiters = 0
	}
}

// acquire blocks until a slot is available for addr or ctx is cancelled.
// Returns ctx.Err() if cancelled while waiting.
func (q *verifierHostQueue) acquire(ctx context.Context, addr string) error {
	st := q.state(addr)
	q.addWaiter(addr, 1)
	defer q.addWaiter(addr, -1)
	select {
	case st.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release returns one slot to addr's semaphore. Must be called exactly once
// after a successful acquire.
func (q *verifierHostQueue) release(addr string) {
	st := q.state(addr)
	<-st.sem
}

func (q *verifierHostQueue) beginInflight(addr string, rec inflightVerify) {
	q.mu.Lock()
	defer q.mu.Unlock()
	st := q.slots[addr]
	if st == nil {
		return
	}
	st.inflight = append(st.inflight, rec)
}

func (q *verifierHostQueue) endInflight(addr string, rec inflightVerify) {
	q.mu.Lock()
	defer q.mu.Unlock()
	st := q.slots[addr]
	if st == nil {
		return
	}
	for i, existing := range st.inflight {
		if existing.Escrow == rec.Escrow && existing.Nonce == rec.Nonce && existing.SentAt.Equal(rec.SentAt) && existing.RequestID == rec.RequestID {
			st.inflight = append(st.inflight[:i], st.inflight[i+1:]...)
			return
		}
	}
}

// snapshot reports who is holding addr's slots and how many rounds are still queued behind them.
//
// A round that calls this because its own wait expired is no longer counted: acquire drops its waiter
// on the way out, so waiters means "others still queued", not queue depth including the caller. On the
// line that reports a giving-up round that reads correctly — inflight=1 waiters=0 says one holder,
// nobody else behind it, and me timing out.
func (q *verifierHostQueue) snapshot(addr string) (inflight []inflightVerify, waiters int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	st := q.slots[addr]
	if st == nil {
		return nil, 0
	}
	inflight = append([]inflightVerify(nil), st.inflight...)
	return inflight, st.waiters
}

func formatInflightSnapshot(inflight []inflightVerify, now time.Time) string {
	if len(inflight) == 0 {
		return ""
	}
	sorted := append([]inflightVerify(nil), inflight...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].SentAt.Before(sorted[j].SentAt)
	})
	if len(sorted) > inflightSnapshotLimit {
		sorted = sorted[:inflightSnapshotLimit]
	}
	parts := make([]string, 0, len(sorted))
	for _, rec := range sorted {
		ageMs := max(now.Sub(rec.SentAt).Milliseconds(), 0)
		var b strings.Builder
		if rec.RequestID != "" {
			fmt.Fprintf(&b, "request=%s ", rec.RequestID)
		}
		fmt.Fprintf(&b, "escrow=%s nonce=%d reason=%s sent_age_ms=%d", rec.Escrow, rec.Nonce, rec.Reason, ageMs)
		parts = append(parts, b.String())
	}
	return strings.Join(parts, "; ")
}

func formatErrorClasses(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, counts[k]))
	}
	return strings.Join(parts, ",")
}
