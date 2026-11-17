package escrow

import (
	"math"
	"sync"
	"time"

	"devshard/cmd/gateway/scheduler"
)

const unboundedInFlight = math.MaxInt

// inFlightSet dedups concurrent operations by key. See README.md, "Keeping the request path off the chain".
type inFlightSet struct {
	mu   sync.Mutex
	keys map[string]bool
}

func (s *inFlightSet) enter(key string) (leave func(), busy bool) {
	leave, admitted := s.enterWithin(key, unboundedInFlight)
	return leave, !admitted
}

func (s *inFlightSet) enterWithin(key string, limit int) (leave func(), admitted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		s.keys = make(map[string]bool)
	}
	if s.keys[key] || len(s.keys) >= limit {
		return nil, false
	}
	s.keys[key] = true
	return func() {
		s.mu.Lock()
		delete(s.keys, key)
		s.mu.Unlock()
	}, true
}

func (s *inFlightSet) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keys[key]
}

// markSet is the request-path-to-tick handoff. See README.md, "Keeping the request path off the chain".
type markSet struct {
	mu   sync.Mutex
	keys map[string]bool
}

// mark reports whether the key was new to this tick.
func (s *markSet) mark(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		s.keys = make(map[string]bool)
	}
	if s.keys[key] {
		return false
	}
	s.keys[key] = true
	return true
}

func (s *markSet) forget(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, key)
}

func (s *markSet) retain(keep func(key string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.keys {
		if !keep(key) {
			delete(s.keys, key)
		}
	}
}

func (s *markSet) drain() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	marks := s.keys
	s.keys = nil
	return marks
}

type retryWindows struct {
	mu     sync.Mutex
	opened map[string]time.Time
}

func (w *retryWindows) retrying(key string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	opened, seen := w.opened[key]
	if !seen {
		if w.opened == nil {
			w.opened = make(map[string]time.Time)
		}
		opened = now
		w.opened[key] = opened
	}
	return now.Sub(opened) < failedSweepRetryWindow
}

func (w *retryWindows) forget(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.opened, key)
}

// depletionMarks is markSet with the reason kept, so the tick can tell a nonce cap from a balance floor.
type depletionMarks struct {
	mu      sync.Mutex
	reasons map[string]scheduler.ExhaustionReason
}

// mark reports whether the escrow was new to this tick; a nonce cap overwrites a balance reason, never the reverse.
func (s *depletionMarks) mark(escrowID string, reason scheduler.ExhaustionReason) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reasons == nil {
		s.reasons = make(map[string]scheduler.ExhaustionReason)
	}
	previous, seen := s.reasons[escrowID]
	if !seen || (reason == scheduler.ExhaustionNonceCap && previous != scheduler.ExhaustionNonceCap) {
		s.reasons[escrowID] = reason
	}
	return !seen
}

func (s *depletionMarks) drain() map[string]scheduler.ExhaustionReason {
	s.mu.Lock()
	defer s.mu.Unlock()
	reasons := s.reasons
	s.reasons = nil
	return reasons
}
