package syncx

import "sync"

// Keyed hands out one lock per key. Keys are never dropped, so use it only where the
// set of keys is bounded, such as a host's own nodes
type Keyed[K comparable] struct {
	locks sync.Map
}

// Lock blocks until the key is free and returns the release
func (k *Keyed[K]) Lock(key K) func() {
	held, _ := k.locks.LoadOrStore(key, &sync.Mutex{})

	lock := held.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}
