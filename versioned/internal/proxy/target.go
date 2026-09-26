package proxy

import (
	"net/http"
	"sync"
)

// RouteTable maps a public version name to one concrete child generation.
type RouteTable map[string]*Target

// Target tracks requests pinned by the proxy to one child process.
type Target struct {
	address  string
	childH2C bool

	mu       sync.Mutex
	retired  bool
	inflight int
	drained  chan struct{}
	closed   bool
}

// NewTarget registers a child that accepts prior-knowledge HTTP/2.
func NewTarget(address string) *Target {
	return NewChildTarget(address, true)
}

// NewChildTarget registers a child. childH2C selects the dial: HTTP/2 when
// the binary advertised an h2c listen, HTTP/1.1 otherwise.
func NewChildTarget(address string, childH2C bool) *Target {
	return &Target{
		address:  address,
		childH2C: childH2C,
		drained:  make(chan struct{}),
	}
}

func (t *Target) transport() http.RoundTripper {
	if t != nil && t.childH2C {
		return childTransport
	}
	return childHTTP1Transport
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
