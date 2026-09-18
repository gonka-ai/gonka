package syncx

import "sync"

// Keyed hands out one lock per key and forgets a key nobody holds or waits for, so it is
// safe for an unbounded set of keys such as request ids
type Keyed[K comparable] struct {
	mu    sync.Mutex
	locks map[K]*keyedLock
}

type keyedLock struct {
	mu      sync.Mutex
	holders int
}

// Lock blocks until the key is free and returns the release
func (k *Keyed[K]) Lock(key K) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[K]*keyedLock)
	}
	held, found := k.locks[key]
	if !found {
		held = &keyedLock{}
		k.locks[key] = held
	}
	held.holders++
	k.mu.Unlock()

	held.mu.Lock()
	return func() {
		held.mu.Unlock()

		k.mu.Lock()
		defer k.mu.Unlock()
		held.holders--
		if held.holders == 0 {
			delete(k.locks, key)
		}
	}
}
