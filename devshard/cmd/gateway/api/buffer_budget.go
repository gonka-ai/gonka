package api

import (
	"errors"
	"sync"
)

var ErrResponseBufferFull = errors.New("the gateway is holding as many buffered replies as it can")

type BufferBudget struct {
	mu    sync.Mutex
	held  int64
	limit int64
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
	if b.limit > 0 && b.held+bytes > b.limit {
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
	b.held -= bytes
	if b.held < 0 {
		b.held = 0
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
