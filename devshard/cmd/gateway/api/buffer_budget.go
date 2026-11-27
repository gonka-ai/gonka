package api

import (
	"errors"
	"sync"
)

var (
	ErrResponseBufferFull = errors.New("the gateway is holding as many buffered replies as it can")
	ErrRequestBodiesFull  = errors.New("the gateway is reading as many request bodies as it can")
)

type BufferBudget struct {
	mu      sync.Mutex
	held    int64
	limit   int64
	callers map[string]int64
}

func NewBufferBudget(limit int64) *BufferBudget {
	if limit < 0 {
		limit = 0
	}
	return &BufferBudget{limit: limit}
}

func (b *BufferBudget) reserve(bytes int64) bool {
	if !b.accountsFor(bytes) {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.fitsLocked(bytes) {
		return false
	}
	b.held += bytes
	return true
}

func (b *BufferBudget) release(bytes int64) {
	if !b.accountsFor(bytes) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.giveBackLocked(bytes)
}

func (b *BufferBudget) fitsLocked(bytes int64) bool {
	return b.limit <= 0 || b.held+bytes <= b.limit
}

func (b *BufferBudget) giveBackLocked(bytes int64) {
	b.held = max(b.held-bytes, 0)
}

type callerShare struct {
	budget *BufferBudget
	caller string
	limit  int64
}

func (b *BufferBudget) forCaller(caller string, limit int64) *callerShare {
	return &callerShare{budget: b, caller: caller, limit: limit}
}

func (s *callerShare) reserve(bytes int64) bool {
	if s == nil || !s.budget.accountsFor(bytes) {
		return true
	}
	b := s.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.fitsLocked(bytes) {
		return false
	}
	if s.limit > 0 && b.callers[s.caller]+bytes > s.limit {
		return false
	}
	if b.callers == nil {
		b.callers = make(map[string]int64)
	}
	b.held += bytes
	b.callers[s.caller] += bytes
	return true
}

func (s *callerShare) release(bytes int64) {
	if s == nil || !s.budget.accountsFor(bytes) {
		return
	}
	b := s.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	b.giveBackLocked(bytes)
	if remaining := b.callers[s.caller] - bytes; remaining > 0 {
		b.callers[s.caller] = remaining
	} else {
		delete(b.callers, s.caller)
	}
}

// accountsFor reports a non-nil budget and a positive amount to reserve or release.
func (b *BufferBudget) accountsFor(bytes int64) bool {
	return b != nil && bytes > 0
}

func (b *BufferBudget) Held() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.held
}

func (b *BufferBudget) Retune(limit int64) {
	if b == nil {
		return
	}
	if limit < 0 {
		limit = 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.limit = limit
}
