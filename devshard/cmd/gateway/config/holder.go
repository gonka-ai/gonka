package config

import (
	"sync"
	"sync/atomic"
)

// Holder publishes the live configuration snapshot. See README.md, "Using the `Holder`".
type Holder struct {
	current     atomic.Pointer[Config]
	mutex       sync.Mutex
	subscribers map[int]func(*Config)
	nextID      int
}

func NewHolder(initial *Config) *Holder {
	holder := &Holder{subscribers: make(map[int]func(*Config))}
	holder.current.Store(initial)
	return holder
}

// Load returns the current snapshot. The result is shared and immutable.
func (h *Holder) Load() *Config { return h.current.Load() }

// Swap publishes a new snapshot and notifies subscribers unordered, so they must be order-independent.
func (h *Holder) Swap(next *Config) {
	h.current.Store(next)
	h.mutex.Lock()
	callbacks := make([]func(*Config), 0, len(h.subscribers))
	for _, callback := range h.subscribers {
		callbacks = append(callbacks, callback)
	}
	h.mutex.Unlock()
	for _, callback := range callbacks {
		callback(next)
	}
}

func (h *Holder) Subscribe(callback func(*Config)) (cancel func()) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	id := h.nextID
	h.nextID++
	h.subscribers[id] = callback
	return func() {
		h.mutex.Lock()
		defer h.mutex.Unlock()
		delete(h.subscribers, id)
	}
}
