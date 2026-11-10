package proxy

import "sync"

// RouteTable maps a public version name to one concrete child generation.
type RouteTable map[string]*Target

// Target tracks requests pinned by the proxy to one child process.
type Target struct {
	address string

	mu       sync.Mutex
	retired  bool
	inflight int
	drained  chan struct{}
	closed   bool
}

func NewTarget(address string) *Target {
	return &Target{
		address: address,
		drained: make(chan struct{}),
	}
}

func (t *Target) Address() string {
	return t.address
}

func (t *Target) acquire() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.retired {
		return false
	}
	t.inflight++
	return true
}

func (t *Target) release() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inflight--
	t.closeDrainedLocked()
}

// Retire prevents new proxy requests from using the target. The returned
// channel closes after every request that already acquired the target exits.
func (t *Target) Retire() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.retired = true
	t.closeDrainedLocked()
	return t.drained
}

func (t *Target) closeDrainedLocked() {
	if t.retired && t.inflight == 0 && !t.closed {
		close(t.drained)
		t.closed = true
	}
}
