package apiconfig

import "sync"

// RuntimeConfigNotifier is a fan-out broadcast for "params_block_height advanced".
// Notify closes the current channel and installs a fresh one, waking every waiter
// that subscribed via NotifyChan before the close.
type RuntimeConfigNotifier struct {
	mu sync.Mutex
	ch chan struct{}
}

func NewRuntimeConfigNotifier() *RuntimeConfigNotifier {
	return &RuntimeConfigNotifier{ch: make(chan struct{})}
}

// NotifyChan returns a channel that is closed on the next Notify call.
// After waking, callers must call NotifyChan again to wait for the next event.
func (n *RuntimeConfigNotifier) NotifyChan() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ch
}

// Notify wakes all current receivers.
func (n *RuntimeConfigNotifier) Notify() {
	n.mu.Lock()
	defer n.mu.Unlock()
	close(n.ch)
	n.ch = make(chan struct{})
}
