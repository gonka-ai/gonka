package transport

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"devshard/host"
)

const (
	// peerLookupWait bounds how long a repair probe or timeout verify waits
	// for a slot's client. A lookup that outlives the wait keeps running and
	// fills the slot for the next caller.
	peerLookupWait = DefaultLazyWait
	// peerLookupRetry is how long a failed lookup answers for the slot before
	// the resolver runs again.
	peerLookupRetry = DefaultLazyRetry
)

var (
	// ErrPeerURLMissing is a slot whose participant publishes no
	// InferenceUrl. Nobody can dial that executor, so timeout verify treats
	// it as unreachable.
	ErrPeerURLMissing = errors.New("peer has no inference url")
	// ErrPeerUnresolved is a timeout verify that has no client for the
	// executor because the URL lookup failed or did not answer in time. The
	// verifier abstains: accepting would vote against an executor it never
	// challenged.
	ErrPeerUnresolved = errors.New("executor peer client unresolved")

	errPeerClientsClosed = errors.New("peer clients closed")
)

// PeerResolver builds the client for one slot. A chain answer with no URL
// is ErrPeerURLMissing (wrapped); any other error is treated as transient.
type PeerResolver func(slot int) (HostPeerClient, error)

type peerRoster struct {
	mu      sync.Mutex
	slots   map[int]*Lazy[HostPeerClient]
	resolve PeerResolver
	closed  bool
}

func closePeer(pc HostPeerClient) {
	if pc != nil {
		pc.Close()
	}
}

// setClients replaces the roster with peers. Replaced slots still waiting
// on a lookup are closed so a late result is released; resolved clients
// stay owned by whoever handed them in.
func (r *peerRoster) setClients(peers map[int]HostPeerClient) {
	var slots map[int]*Lazy[HostPeerClient]
	if peers != nil {
		slots = make(map[int]*Lazy[HostPeerClient], len(peers))
		for slot, pc := range peers {
			if pc != nil {
				slots[slot] = ReadyLazy(pc, closePeer)
			}
		}
	}
	r.mu.Lock()
	old := r.slots
	r.slots = slots
	r.mu.Unlock()
	for _, l := range old {
		if _, held := l.Peek(); !held {
			l.Close()
		}
	}
}

// SetPeerResolver fills slots that SetPeerClients left empty on first use,
// so a session opens while a neighbour's URL is still unknown.
func (s *Server) SetPeerResolver(resolve PeerResolver) {
	s.peers.mu.Lock()
	s.peers.resolve = resolve
	s.peers.mu.Unlock()
	if s.host != nil {
		s.host.SetRepairProbe(s.RepairProbe)
	}
}

// StartPeerLookups resolves slots in the background so the first repair
// probe or timeout vote usually finds the client ready. It does not wait.
func (s *Server) StartPeerLookups(slots ...int) {
	for _, slot := range slots {
		if l, _ := s.peerSlot(slot); l != nil {
			l.Start()
		}
	}
}

// peerSlot returns slot's entry, creating it from the resolver. nil is a
// slot with no client and no resolver.
func (s *Server) peerSlot(slot int) (*Lazy[HostPeerClient], error) {
	r := &s.peers
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errPeerClientsClosed
	}
	if l := r.slots[slot]; l != nil {
		return l, nil
	}
	if r.resolve == nil {
		return nil, nil
	}
	if r.slots == nil {
		r.slots = make(map[int]*Lazy[HostPeerClient])
	}
	l := newPeerLazy(slot, r.resolve)
	r.slots[slot] = l
	return l, nil
}

func newPeerLazy(slot int, resolve PeerResolver) *Lazy[HostPeerClient] {
	build := func() (HostPeerClient, error) {
		pc, err := resolve(slot)
		if err == nil && pc == nil {
			err = ErrPeerURLMissing
		}
		if err != nil {
			return nil, err
		}
		return pc, nil
	}
	return NewLazy(build, closePeer, peerLookupWait, peerLookupRetry)
}

// peerClient returns slot's client and resolves it when absent. (nil, nil)
// is a slot with no client and no resolver. Concurrent callers share one
// lookup; each waits at most peerLookupWait or until ctx ends.
func (s *Server) peerClient(ctx context.Context, slot int) (HostPeerClient, error) {
	l, err := s.peerSlot(slot)
	if err != nil || l == nil {
		return nil, err
	}
	pc, err := l.Get(ctx)
	if errors.Is(err, ErrLazyClosed) {
		return nil, errPeerClientsClosed
	}
	return pc, err
}

// timeoutExecutor is the executor's client for a timeout vote. A slot with
// no published URL yields nil, which the verify treats as unreachable. Any
// other lookup failure is this verifier's own blind spot and abstains.
func (s *Server) timeoutExecutor(ctx context.Context, slot int) (host.ExecutorClient, error) {
	pc, err := s.peerClient(ctx, slot)
	switch {
	case err == nil && pc != nil:
		return pc, nil
	case err == nil, errors.Is(err, ErrPeerURLMissing):
		return nil, nil
	default:
		return nil, fmt.Errorf("%w: slot %d: %w", ErrPeerUnresolved, slot, err)
	}
}
