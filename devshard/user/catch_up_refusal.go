package user

import (
	"context"
	"sync"
	"time"
)

const (
	catchUpRefusalFirstBackoff = time.Second
	catchUpRefusalMaxBackoff   = 30 * time.Second
)

// catchUpRefusal remembers a host's last catch-up refusal, so a host that keeps refusing is not asked again on every
// send and every timeout vote, each time with a chunk of diffs read from the store.
type catchUpRefusal struct {
	mu      sync.Mutex
	refused error
	retryAt time.Time
	backoff time.Duration
}

// standing is the host's last refusal while its backoff runs, else nil.
func (r *catchUpRefusal) standing(now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refused == nil || !now.Before(r.retryAt) {
		return nil
	}
	return r.refused
}

// record keeps a refusal and doubles its backoff; a success clears both. A catch-up given up by its own context
// says nothing about the host.
func (r *catchUpRefusal) record(ctx context.Context, err error, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		r.refused, r.retryAt, r.backoff = nil, time.Time{}, 0
		return
	}
	r.backoff = min(max(2*r.backoff, catchUpRefusalFirstBackoff), catchUpRefusalMaxBackoff)
	r.refused, r.retryAt = err, now.Add(r.backoff)
}

// hostCatchUpRefusal is the one refusal memory of a host index.
func (s *Session) hostCatchUpRefusal(hostIdx int) *catchUpRefusal {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.catchUpRefusals == nil {
		s.catchUpRefusals = make(map[int]*catchUpRefusal)
	}
	refusal, ok := s.catchUpRefusals[hostIdx]
	if !ok {
		refusal = &catchUpRefusal{}
		s.catchUpRefusals[hostIdx] = refusal
	}
	return refusal
}
