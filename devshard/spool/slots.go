package spool

import "sync"

// Slots is a reset-safe counting semaphore. SetMax never zeroes cur, and
// TryAcquire always counts holders (even when max == 0 / unlimited), so a
// retune while slots are held cannot inflate available capacity.
type Slots struct {
	mu  sync.Mutex
	max int64
	cur int64
}

func NewSlots(max int64) *Slots {
	if max < 0 {
		max = 0
	}
	return &Slots{max: max}
}

func (s *Slots) TryAcquire() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Unlimited (max == 0): skip the cap, but still count holders so a later
	// SetMax to a finite n cannot inflate available capacity.
	if s.max >= 1 && s.cur >= s.max {
		return false
	}
	s.cur++
	return true
}

func (s *Slots) Release() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur > 0 {
		s.cur--
	}
}

func (s *Slots) SetMax(n int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 0 {
		n = 0
	}
	s.max = n
}

func (s *Slots) Stats() (max, cur int64) {
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.max, s.cur
}

// Snapshot is an alias of Stats for callers that mirror the former aggregate
// semaphore API.
func (s *Slots) Snapshot() (max, cur int64) {
	return s.Stats()
}

// Restore sets max and cur. Intended for tests that save/restore process state.
func (s *Slots) Restore(max, cur int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if max < 0 {
		max = 0
	}
	if cur < 0 {
		cur = 0
	}
	s.max = max
	s.cur = cur
}
