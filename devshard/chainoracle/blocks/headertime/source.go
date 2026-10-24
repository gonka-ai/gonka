package headertime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"common/chainoracle/blocks"
	"devshard/types"
)

const (
	DefaultCapacity     = 200_000
	DefaultFetchTimeout = 5 * time.Second
	DefaultMissWait     = 250 * time.Millisecond
)

type HistoryOracle interface {
	HeaderAt(ctx context.Context, height int64) (*blocks.Header, error)
}

type WaitingOracle interface {
	HeaderAtWait(ctx context.Context, height int64, wait time.Duration) (*blocks.Header, error)
}

type entry struct {
	hash []byte
	ms   int64
}

type Source struct {
	oracle       blocks.BlockOracle
	fetchTimeout time.Duration
	missWait     time.Duration
	capacity     int

	mu       sync.Mutex
	entries  map[uint64]entry
	order    []uint64
	inflight map[uint64]chan struct{}
}

type Option func(*Source)

func WithFetchTimeout(d time.Duration) Option {
	return func(s *Source) { s.fetchTimeout = d }
}

func WithMissWait(d time.Duration) Option {
	return func(s *Source) { s.missWait = d }
}

func WithCapacity(n int) Option {
	return func(s *Source) { s.capacity = n }
}

func New(oracle blocks.BlockOracle, opts ...Option) *Source {
	s := &Source{
		oracle:       oracle,
		fetchTimeout: DefaultFetchTimeout,
		missWait:     DefaultMissWait,
		capacity:     DefaultCapacity,
		entries:      make(map[uint64]entry),
		inflight:     make(map[uint64]chan struct{}),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *Source) Observe(h *blocks.Header) {
	if s == nil || h == nil || h.Height <= 0 || h.Time.IsZero() || len(h.BlockHash) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putLocked(uint64(h.Height), h.BlockHash, h.Time.UnixMilli())
}

func (s *Source) HeaderTimeMs(height uint64, hash []byte) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("%w: no header time source", types.ErrHeaderPending)
	}
	if height == 0 || len(hash) == 0 {
		return 0, fmt.Errorf("%w: empty floor point", types.ErrHeaderPending)
	}
	s.mu.Lock()
	e, ok := s.entries[height]
	if ok {
		s.mu.Unlock()
		return checkEntry(height, hash, e)
	}
	done, running := s.inflight[height]
	if !running {
		done = make(chan struct{})
		s.inflight[height] = done
		go s.fetchInto(height, done)
	}
	s.mu.Unlock()

	timer := time.NewTimer(s.missWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		return 0, fmt.Errorf("%w: height %d: lookup still running", types.ErrHeaderPending, height)
	}
	s.mu.Lock()
	e, ok = s.entries[height]
	s.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("%w: height %d", types.ErrHeaderPending, height)
	}
	return checkEntry(height, hash, e)
}

func (s *Source) AwaitHeaderTimeMs(ctx context.Context, height uint64, hash []byte) (int64, error) {
	for {
		ms, err := s.HeaderTimeMs(height, hash)
		if !errors.Is(err, types.ErrHeaderPending) {
			return ms, err
		}
		s.mu.Lock()
		done, running := s.inflight[height]
		s.mu.Unlock()
		if !running {
			return ms, err
		}
		select {
		case <-ctx.Done():
			return 0, err
		case <-done:
		}
	}
}

func (s *Source) fetchInto(height uint64, done chan struct{}) {
	defer func() {
		s.mu.Lock()
		delete(s.inflight, height)
		s.mu.Unlock()
		close(done)
	}()
	h, err := s.fetch(int64(height))
	if err != nil || h == nil || blocks.IsDummyHeader(h) || h.Height != int64(height) || h.Time.IsZero() || len(h.BlockHash) == 0 {
		return
	}
	s.mu.Lock()
	s.putLocked(height, h.BlockHash, h.Time.UnixMilli())
	s.mu.Unlock()
}

func checkEntry(height uint64, hash []byte, e entry) (int64, error) {
	if !bytes.Equal(e.hash, hash) {
		return 0, fmt.Errorf("%w: height %d", types.ErrHeaderMismatch, height)
	}
	return e.ms, nil
}

func (s *Source) fetch(height int64) (*blocks.Header, error) {
	if s.oracle == nil {
		return nil, fmt.Errorf("no block oracle")
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.fetchTimeout)
	defer cancel()
	wait := s.fetchTimeout - time.Second
	if w, ok := s.oracle.(WaitingOracle); ok && wait > 0 {
		return w.HeaderAtWait(ctx, height, wait)
	}
	if w, ok := s.oracle.(blocks.HeaderWaiter); ok && wait > 0 {
		return w.AtWait(ctx, height, wait)
	}
	if hist, ok := s.oracle.(HistoryOracle); ok {
		return hist.HeaderAt(ctx, height)
	}
	return s.oracle.At(ctx, height)
}

func (s *Source) putLocked(height uint64, hash []byte, ms int64) {
	if _, ok := s.entries[height]; !ok {
		s.order = append(s.order, height)
	}
	s.entries[height] = entry{hash: append([]byte(nil), hash...), ms: ms}
	if s.capacity > 0 && len(s.order) > s.capacity {
		drop := len(s.order) - s.capacity
		for _, h := range s.order[:drop] {
			delete(s.entries, h)
		}
		s.order = append([]uint64(nil), s.order[drop:]...)
	}
}
